import Alert from "@mui/material/Alert";
import Chip from "@mui/material/Chip";
import LinearProgress from "@mui/material/LinearProgress";
import Paper from "@mui/material/Paper";
import Stack from "@mui/material/Stack";
import Table from "@mui/material/Table";
import TableBody from "@mui/material/TableBody";
import TableCell from "@mui/material/TableCell";
import TableContainer from "@mui/material/TableContainer";
import TableHead from "@mui/material/TableHead";
import TableRow from "@mui/material/TableRow";
import Typography from "@mui/material/Typography";

import { ago, at, backups } from "./api";
import type { BackupInfo, BackupRun, GetBackupStatusResponse, GetRestoreStatusResponse, RestoreRun } from "./gen/sluis/v1/backup_pb";
import { counts, freshness, freshnessNote, maintenanceLine, newestFirst, progress, restoreHowTo, retentionText, runState } from "./backupModel";
import { useAsync, useWhile } from "./hooks";
import { Failure, Facts, Loading, Mono, Nothing, Page, Section } from "./ui";

/** How often an unfinished run is asked about again, in milliseconds. */
const pollMs = 15_000;

/** Backups, as the backup module and the restore function report them. The
 *  page only reads: it starts no backup and no restore, and the modules would
 *  refuse the console if it tried. */
export function BackupsPage() {
  const status = useAsync(() => backups.getBackupStatus({}), []);
  const list = useAsync(() => backups.listBackups({}), []);
  const restore = useAsync(() => backups.getRestoreStatus({}), []);
  // A run that is not finished moves; a steady state is not polled.
  const moving = Boolean(status.value?.unfinished) || Boolean(restore.value?.unfinished);
  useWhile(moving, pollMs, () => {
    status.reload();
    restore.reload();
  });
  return (
    <Page title="Backups" lede="The installation's encrypted backups in the archive, and how the last restore stands. This page reads; it changes nothing.">
      <Latest status={status.value} loading={status.loading} error={status.error} />
      <Unfinished run={status.value?.unfinished} />
      <Archive loading={list.loading} error={list.error} available={list.value?.available ?? true} items={list.value?.backups ?? []} />
      <Restore status={restore.value} loading={restore.loading} error={restore.error} />
    </Page>
  );
}

function RunChip({ state }: { state: string }) {
  const s = runState(state);
  return <Chip size="small" label={s.label} color={s.tone} variant={s.tone === "error" ? "filled" : "outlined"} />;
}

function Latest({ status, loading, error }: { status?: GetBackupStatusResponse; loading: boolean; error?: string }) {
  const f = freshness(status?.lastCompleted, new Date());
  const latest = status?.latest;
  return (
    <Section title="Latest backup" hint="The newest run, whatever its state, and the newest that completed.">
      <Loading busy={loading} />
      <Failure error={error} />
      {status && !status.available ? <Nothing>This deployment has no backup module.</Nothing> : null}
      {status?.available ? (
        <Stack spacing={1.5}>
          {f !== "fresh" ? <Alert severity={f === "stale" ? "warning" : "info"}>{freshnessNote(f)}</Alert> : null}
          {latest ? (
            <Facts
              items={[
                { label: "Run", value: <Mono>{latest.id}</Mono> },
                { label: "State", value: <RunChip state={latest.state} /> },
                { label: "Started", value: when(latest) },
                { label: "Trigger", value: latest.trigger },
                { label: "Contents", value: counts(latest) },
                { label: "Last completed", value: status.lastCompleted ? <Mono>{status.lastCompleted.id}</Mono> : "never" },
              ]}
            />
          ) : (
            <Nothing>No backup has run yet.</Nothing>
          )}
          {latest?.error || latest?.reason ? <Alert severity={latest.state === "failed" ? "error" : "info"}>{latest.error || latest.reason}</Alert> : null}
          <Typography variant="body2" color="text.secondary">
            {retentionText(status.retention)}
          </Typography>
        </Stack>
      ) : null}
    </Section>
  );
}

function when(run: BackupRun): string {
  const started = at(run.started);
  return started ? `${started.toISOString().replace("T", " ").replace(/\.\d+Z$/, " UTC")} (${ago(started)})` : "—";
}

function Unfinished({ run }: { run?: BackupRun }) {
  if (!run) return null;
  const done = progress(run);
  return (
    <Section title="Unfinished run" hint="A run that is running or paused continues from its checkpoint.">
      <Stack spacing={1}>
        <Facts items={[{ label: "Run", value: <Mono>{run.id}</Mono> }, { label: "State", value: <RunChip state={run.state} /> }, { label: "Progress", value: done ?? "not reported" }]} />
        {run.units > 0 ? <LinearProgress variant="determinate" value={Math.min(100, (run.done * 100) / run.units)} /> : null}
      </Stack>
    </Section>
  );
}

function Archive({ loading, error, available, items }: { loading: boolean; error?: string; available: boolean; items: BackupInfo[] }) {
  const rows = newestFirst(items);
  return (
    <Section title="Backups in the archive" hint="Newest first, from each backup's manifest. A manifest is not verified here: treat these as information.">
      <Loading busy={loading} />
      <Failure error={error} />
      {!available ? <Nothing>This deployment has no backup module.</Nothing> : null}
      {available && !loading && !error && rows.length === 0 ? <Nothing>The archive holds no backup.</Nothing> : null}
      {rows.length > 0 ? (
        <TableContainer component={Paper} variant="outlined" sx={{ overflowX: "auto" }}>
          <Table size="small" sx={{ minWidth: 640 }}>
            <TableHead>
              <TableRow>
                <TableCell>Backup</TableCell>
                <TableCell>Created</TableCell>
                <TableCell>Layout</TableCell>
                <TableCell>Contents</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {rows.map((b) => {
                const created = at(b.created);
                return (
                  <TableRow key={b.id} hover>
                    <TableCell>
                      <Mono>{b.id}</Mono>
                    </TableCell>
                    <TableCell>{created ? ago(created) : "—"}</TableCell>
                    <TableCell>{b.layout || "—"}</TableCell>
                    <TableCell>
                      {counts(b)}
                      {b.error ? (
                        <Typography variant="caption" color="warning.main" sx={{ display: "block" }}>
                          {b.error}
                        </Typography>
                      ) : null}
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </TableContainer>
      ) : null}
    </Section>
  );
}

function Restore({ status, loading, error }: { status?: GetRestoreStatusResponse; loading: boolean; error?: string }) {
  const shown: RestoreRun | undefined = status?.unfinished ?? status?.latest;
  return (
    <Section title="Restore" hint="The latest restore and the modules that are under maintenance because of one.">
      <Loading busy={loading} />
      <Failure error={error} />
      {status && !status.available ? <Nothing>This deployment has no restore function.</Nothing> : null}
      {status?.available ? (
        <Stack spacing={1.5}>
          {status.maintenance.length > 0 ? (
            <Alert severity="warning">
              {status.maintenance.map((m) => (
                <div key={m.module}>{maintenanceLine(m)}</div>
              ))}
            </Alert>
          ) : null}
          {shown ? (
            <Facts
              items={[
                { label: "Restore", value: <Mono>{shown.id}</Mono> },
                { label: "State", value: <RunChip state={shown.state} /> },
                { label: "Backup", value: <Mono>{shown.backupId}</Mono> },
                { label: "Started", value: ago(at(shown.started)) },
                { label: "By", value: shown.by },
                { label: "Note", value: shown.note },
                { label: "Overwrite", value: shown.overwrite ? "yes" : "no" },
              ]}
            />
          ) : (
            <Nothing>No restore has been run.</Nothing>
          )}
          {shown?.error || shown?.reason ? <Alert severity={shown.state === "failed" ? "error" : "info"}>{shown.error || shown.reason}</Alert> : null}
        </Stack>
      ) : null}
      <Typography variant="body2" color="text.secondary" sx={{ mt: 1.5 }}>
        {restoreHowTo}
      </Typography>
    </Section>
  );
}
