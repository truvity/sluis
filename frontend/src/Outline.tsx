import { Fragment, useMemo, useState, type ReactNode } from "react";
import Alert from "@mui/material/Alert";
import Box from "@mui/material/Box";
import Button from "@mui/material/Button";
import Dialog from "@mui/material/Dialog";
import DialogActions from "@mui/material/DialogActions";
import DialogContent from "@mui/material/DialogContent";
import DialogContentText from "@mui/material/DialogContentText";
import DialogTitle from "@mui/material/DialogTitle";
import FormControlLabel from "@mui/material/FormControlLabel";
import Paper from "@mui/material/Paper";
import Stack from "@mui/material/Stack";
import Switch from "@mui/material/Switch";
import Table from "@mui/material/Table";
import TableBody from "@mui/material/TableBody";
import TableCell from "@mui/material/TableCell";
import TableContainer from "@mui/material/TableContainer";
import TableHead from "@mui/material/TableHead";
import TableRow from "@mui/material/TableRow";
import TextField from "@mui/material/TextField";
import Typography from "@mui/material/Typography";

import { ago, at, outline, reason } from "./api";
import type { OutlineWorkspaceStatus } from "./gen/sluis/v1/outline_pb";
import { useAsync } from "./hooks";
import {
  breakerSentence,
  keyUserNotice,
  narrowed,
  outcomeView,
  parseReport,
  passNotice,
  permissionChange,
  roleChange,
  stateView,
  tripped,
  when,
  type OutlineBreaker,
  type OutlineReport,
} from "./outlineModel";
import { ConfirmDialog, Failure, Loading, Mono, Nothing, Page, Section, State } from "./ui";

type Props = { operator: boolean; onDone: (message: string) => void };

type Notice = { severity: "success" | "info" | "warning"; text: string };

/** The Outline workspaces the policy declares: whether each is connected,
 *  whether the controller acts in it or runs it dry, how its last pass went,
 *  and who and what in it is not yet as the policy says.
 *
 *  An operator connects a workspace with an admin's API key, replaces the
 *  key, disconnects it, asks for a pass and confirms a breaker. The key is
 *  sent once and never comes back. */
export function OutlinePage({ operator, onDone }: Props) {
  const loaded = useAsync(() => outline.getOutlineStatus({}), []);
  const workspaces = loaded.value?.workspaces ?? [];
  // Everything is the exception: by default only the rows that are not yet
  // as the policy says are shown.
  const [everything, setEverything] = useState(false);
  return (
    <Page
      title="Outline"
      lede="The Outline workspaces the policy declares by key. Connecting one keeps its URL and an admin's API key; the controller then keeps its people's roles, its managed groups and the collections bound to internal groups in step with the directory. A workspace not enabled in the policy runs dry: every row says what would change."
      actions={
        <FormControlLabel
          control={<Switch checked={!everything} onChange={(event) => setEverything(!event.target.checked)} />}
          label="Show only what needs attention"
        />
      }
    >
      <Loading busy={loaded.loading} />
      <Failure error={loaded.error} />
      {loaded.value && workspaces.length === 0 ? (
        <Nothing>No Outline workspace is declared. Declare one in the policy&apos;s outline table by its key, then connect it here.</Nothing>
      ) : null}
      <Stack sx={{ gap: 3 }}>
        {workspaces.map((ws) => (
          <WorkspaceCard key={ws.workspace} ws={ws} operator={operator} everything={everything} onDone={onDone} reload={loaded.reload} />
        ))}
      </Stack>
    </Page>
  );
}

function WorkspaceCard({
  ws,
  operator,
  everything,
  onDone,
  reload,
}: {
  ws: OutlineWorkspaceStatus;
  operator: boolean;
  everything: boolean;
  onDone: (message: string) => void;
  reload: () => void;
}) {
  const parsed = useMemo(() => parseReport(ws.reportJson), [ws.reportJson]);
  const report = parsed.report;
  const [failure, setFailure] = useState<string | undefined>();
  const [notice, setNotice] = useState<Notice | undefined>();
  const [asking, setAsking] = useState<"connect" | "disconnect" | undefined>();
  const [refreshing, setRefreshing] = useState(false);
  const [confirming, setConfirming] = useState<string | undefined>();

  // Refresh asks the controller for a pass over this workspace now, instead
  // of at its next interval. The server keeps one request a minute.
  const refresh = async () => {
    setRefreshing(true);
    setFailure(undefined);
    setNotice(undefined);
    try {
      const done = await outline.requestOutlinePass({ workspace: ws.workspace });
      const said = passNotice(ws.workspace, done.kept);
      if (done.kept) {
        onDone(said.text);
        reload();
      } else {
        setNotice(said);
      }
    } catch (error) {
      setFailure(reason(error));
    } finally {
      setRefreshing(false);
    }
  };

  const confirm = async (breaker: OutlineBreaker) => {
    setConfirming(breaker.fingerprint);
    setFailure(undefined);
    try {
      await outline.confirmOutlineBreaker({ workspace: ws.workspace, scope: breaker.scope, fingerprint: breaker.fingerprint });
      onDone(`Confirmed: the held set in ${ws.workspace} (${breaker.scope}) goes ahead on the next pass.`);
      reload();
    } catch (error) {
      setFailure(reason(error));
    } finally {
      setConfirming(undefined);
    }
  };

  const buttons: ReactNode[] = [];
  if (operator) {
    if (ws.connected) {
      buttons.push(
        <Button key="refresh" size="small" variant="text" disabled={refreshing} onClick={() => void refresh()}>
          Refresh
        </Button>,
      );
    }
    buttons.push(
      <Button key="connect" size="small" variant={ws.connected ? "text" : "contained"} onClick={() => setAsking("connect")}>
        {ws.connected ? "Replace key" : "Connect"}
      </Button>,
    );
    if (ws.connected) {
      buttons.push(
        <Button key="disconnect" size="small" color="error" variant="text" onClick={() => setAsking("disconnect")}>
          Disconnect
        </Button>,
      );
    }
  }

  const tick = report?.tick;
  const lastPass = when(tick?.at ?? "");
  const outcome = tick?.outcome ? outcomeView(tick.outcome) : undefined;
  const connectedAt = at(ws.connectedAt);
  const breakers = tripped(report);
  const facts: ReactNode[] = [];
  if (ws.connected) {
    if (ws.url) {
      facts.push(
        <a key="url" href={ws.url} target="_blank" rel="noreferrer">
          {ws.url}
        </a>,
      );
    }
    if (ws.keyUser) facts.push(<span key="key">key of <Mono>{ws.keyUser}</Mono></span>);
    if (connectedAt) facts.push(<span key="at">connected {ago(connectedAt)}{ws.connectedBy ? ` by ${ws.connectedBy}` : ""}</span>);
  } else {
    facts.push(<span key="none">not connected: the controller cannot read or change it</span>);
  }
  facts.push(<span key="pass" title={lastPass?.toISOString()}>{lastPass ? `last pass ${ago(lastPass)}` : "no pass reported yet"}</span>);

  return (
    <Paper variant="outlined" sx={{ p: 2 }}>
      <Stack direction="row" sx={{ alignItems: "flex-start", justifyContent: "space-between", flexWrap: "wrap", gap: 2 }}>
        <Box sx={{ minWidth: 0 }}>
          <Stack direction="row" sx={{ alignItems: "center", flexWrap: "wrap", gap: 1 }}>
            <Typography variant="h6" sx={{ fontFamily: "monospace" }}>
              {ws.workspace}
            </Typography>
            {ws.connected ? <State kind="connected" /> : <State kind="not-connected" title="No URL and API key are kept for this workspace: the controller cannot act in it." />}
            {ws.enabled ? (
              <State kind="acts" title="Enabled in the policy (controllers.outline.enabledWorkspaces): the controller makes the changes it derives." />
            ) : (
              <State kind="dry-run" title="Not enabled in the policy (controllers.outline.enabledWorkspaces): every pass derives what it would do and changes nothing." />
            )}
            {outcome ? <State kind={outcome.kind} label={outcome.label} title={outcome.title} /> : null}
          </Stack>
          <Typography variant="caption" color="text.secondary" sx={{ display: "block", mt: 0.5 }}>
            {facts.map((fact, index) => (
              <Fragment key={index}>
                {index > 0 ? " · " : null}
                {fact}
              </Fragment>
            ))}
          </Typography>
        </Box>
        <Stack direction="row" sx={{ gap: 1, flexShrink: 0, flexWrap: "wrap" }}>
          {buttons}
        </Stack>
      </Stack>

      <Stack sx={{ gap: 1.5, mt: 2 }}>
        <Failure error={failure} />
        {notice ? (
          <Alert severity={notice.severity} onClose={() => setNotice(undefined)}>
            {notice.text}
          </Alert>
        ) : null}
        {parsed.error ? <Alert severity="warning">{parsed.error}</Alert> : null}
        {tick?.error ? <Alert severity="error">The last pass failed: {tick.error}</Alert> : null}
        {report?.held ? (
          <Alert severity="warning">
            <strong>The whole workspace is held:</strong> {report.held} Nothing in it changes until a person acts.
          </Alert>
        ) : null}
        {breakers.map((breaker) => (
          <Alert
            key={breaker.scope}
            severity="error"
            action={
              operator ? (
                <Button color="inherit" size="small" disabled={confirming !== undefined} onClick={() => void confirm(breaker)}>
                  Confirm
                </Button>
              ) : null
            }
          >
            <strong>{breakerSentence(breaker)}</strong> {breaker.reason} Read the rows below; confirming lets exactly this set go ahead, and lapses after a day.
          </Alert>
        ))}
        {ws.connected && !report && !parsed.error ? <Nothing>Connected. The controller has not reported on this workspace yet: its first pass says what it finds.</Nothing> : null}
      </Stack>

      {report ? <Report report={report} everything={everything} /> : null}

      {asking === "connect" ? (
        <ConnectDialog
          ws={ws}
          onCancel={() => setAsking(undefined)}
          onConnected={(keyUser, ignored) => {
            setAsking(undefined);
            setNotice(keyUserNotice(keyUser, ignored));
            reload();
          }}
        />
      ) : null}
      {asking === "disconnect" ? (
        <ConfirmDialog
          title={`Disconnect ${ws.workspace}?`}
          confirm="Disconnect"
          danger
          onCancel={() => setAsking(undefined)}
          run={async () => {
            await outline.disconnectOutlineWorkspace({ workspace: ws.workspace });
            setAsking(undefined);
            onDone(`${ws.workspace} is disconnected. Revoke its API key in Outline when you no longer need it.`);
            reload();
          }}
        >
          The controller stops acting in {ws.workspace}. Its URL and API key are forgotten here, with any confirmed breaker and requested pass. Nothing in
          Outline changes: no account, group or collection is touched, and the API key stays valid until it is revoked in Outline.
        </ConfirmDialog>
      ) : null}
    </Paper>
  );
}

/** The last report's rows: people, managed groups, bound collections and
 *  what is only reported. */
function Report({ report, everything }: { report: OutlineReport; everything: boolean }) {
  const shown = narrowed(report, everything);
  const hint = (kept: number, total: number, noun: string) =>
    everything || total === 0 ? undefined : `${kept} of ${total} ${noun} need${kept === 1 ? "s" : ""} attention`;
  const quiet = (total: number, noun: string) =>
    total === 0 ? `No ${noun} reported.` : `Every one of the ${total} ${noun} is as the policy says.`;
  const shares = when(report.shares_scanned_at);
  return (
    <Box sx={{ mt: 3 }}>
      <Section title="People" hint={hint(shown.users.length, report.users.length, "people")}>
        {shown.users.length === 0 ? (
          <Nothing>{quiet(report.users.length, "people")}</Nothing>
        ) : (
          <TableContainer component={Paper} variant="outlined" sx={{ overflowX: "auto" }}>
            <Table size="small" sx={{ minWidth: 560 }}>
              <TableHead>
                <TableRow>
                  <TableCell>Person</TableCell>
                  <TableCell>Role</TableCell>
                  <TableCell>State</TableCell>
                  <TableCell>Reason</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {shown.users.map((user) => (
                  <TableRow key={user.id || user.email} hover>
                    <TableCell>
                      <Mono>{user.email || user.id}</Mono>
                      {user.name ? (
                        <Typography variant="caption" color="text.secondary" sx={{ display: "block" }}>
                          {user.name}
                        </Typography>
                      ) : null}
                    </TableCell>
                    <TableCell>
                      {roleChange(user.role, user.target)}
                      {user.suspended ? (
                        <Typography variant="caption" color="text.secondary" sx={{ display: "block" }}>
                          suspended
                        </Typography>
                      ) : null}
                    </TableCell>
                    <TableCell>
                      <RowState state={user.state} />
                    </TableCell>
                    <TableCell>
                      <Why text={user.reason} />
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </TableContainer>
        )}
      </Section>

      <Section title="Groups" hint={hint(shown.groups.length, report.groups.length, "groups")}>
        {shown.groups.length === 0 ? (
          <Nothing>{quiet(report.groups.length, "groups")}</Nothing>
        ) : (
          <TableContainer component={Paper} variant="outlined" sx={{ overflowX: "auto" }}>
            <Table size="small" sx={{ minWidth: 560 }}>
              <TableHead>
                <TableRow>
                  <TableCell>Group, then member</TableCell>
                  <TableCell>State</TableCell>
                  <TableCell>Reason</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {shown.groups.map((group) => (
                  <Fragment key={group.name}>
                    <TableRow hover>
                      <TableCell>
                        <Mono>
                          <strong>{group.name}</strong>
                        </Mono>
                      </TableCell>
                      <TableCell>
                        <RowState state={group.state} />
                      </TableCell>
                      <TableCell>
                        <Why text={group.reason} />
                      </TableCell>
                    </TableRow>
                    {group.members.map((m) => (
                      <TableRow key={`${group.name}|${m.user_id || m.email}`} hover>
                        <TableCell sx={{ pl: 4 }}>
                          <Mono>{m.email || m.user_id}</Mono>
                        </TableCell>
                        <TableCell>
                          <RowState state={m.state} />
                        </TableCell>
                        <TableCell>
                          <Why text={m.reason} />
                        </TableCell>
                      </TableRow>
                    ))}
                  </Fragment>
                ))}
              </TableBody>
            </Table>
          </TableContainer>
        )}
      </Section>

      <Section title="Collections" hint={hint(shown.collections.length, report.collections.length, "collections")}>
        {shown.collections.length === 0 ? (
          <Nothing>{quiet(report.collections.length, "collections")}</Nothing>
        ) : (
          <TableContainer component={Paper} variant="outlined" sx={{ overflowX: "auto" }}>
            <Table size="small" sx={{ minWidth: 640 }}>
              <TableHead>
                <TableRow>
                  <TableCell>Collection, then grant or member</TableCell>
                  <TableCell>Mode or permission</TableCell>
                  <TableCell>State</TableCell>
                  <TableCell>Reason</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {shown.collections.map((c) => (
                  <Fragment key={c.name}>
                    <TableRow hover>
                      <TableCell>
                        <strong>{c.name}</strong>
                        {c.directory ? (
                          <Typography variant="caption" color="text.secondary" sx={{ display: "block" }}>
                            directory <Mono>{c.directory}</Mono>
                          </Typography>
                        ) : null}
                      </TableCell>
                      <TableCell>{c.mode}</TableCell>
                      <TableCell>
                        <RowState state={c.state} />
                      </TableCell>
                      <TableCell>
                        <Why text={c.reason} />
                      </TableCell>
                    </TableRow>
                    {c.grants.map((g) => (
                      <TableRow key={`${c.name}|group|${g.group_id || g.group}`} hover>
                        <TableCell sx={{ pl: 4 }}>
                          group <Mono>{g.group}</Mono>
                        </TableCell>
                        <TableCell>{permissionChange(g.permission, g.target)}</TableCell>
                        <TableCell>
                          <RowState state={g.state} />
                        </TableCell>
                        <TableCell>
                          <Why text={g.reason} />
                        </TableCell>
                      </TableRow>
                    ))}
                    {c.members.map((m) => (
                      <TableRow key={`${c.name}|member|${m.user_id || m.email}`} hover>
                        <TableCell sx={{ pl: 4 }}>
                          member <Mono>{m.email || m.user_id}</Mono>
                        </TableCell>
                        <TableCell>{m.permission || "—"}</TableCell>
                        <TableCell>
                          <RowState state={m.state} />
                        </TableCell>
                        <TableCell>
                          <Why text={m.reason} />
                        </TableCell>
                      </TableRow>
                    ))}
                  </Fragment>
                ))}
              </TableBody>
            </Table>
          </TableContainer>
        )}
      </Section>

      <Section
        title="Reported"
        hint={`said and never acted on: drifted settings, shares, integrations${shares ? ` · document shares last read ${ago(shares)}` : ""}`}
      >
        {report.reported.length === 0 ? (
          <Nothing>Nothing reported.</Nothing>
        ) : (
          <TableContainer component={Paper} variant="outlined" sx={{ overflowX: "auto" }}>
            <Table size="small" sx={{ minWidth: 560 }}>
              <TableHead>
                <TableRow>
                  <TableCell>Kind</TableCell>
                  <TableCell>Collection</TableCell>
                  <TableCell>Subject</TableCell>
                  <TableCell>Detail</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {report.reported.map((f, index) => (
                  <TableRow key={`${f.kind}|${f.collection}|${f.subject}|${index}`} hover>
                    <TableCell>{f.kind}</TableCell>
                    <TableCell>{f.collection || "—"}</TableCell>
                    <TableCell>{f.subject}</TableCell>
                    <TableCell>
                      <Why text={f.detail} />
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </TableContainer>
        )}
      </Section>
    </Box>
  );
}

function RowState({ state }: { state: string }) {
  const view = stateView(state);
  return <State kind={view.kind} label={view.label} title={view.title} />;
}

function Why({ text }: { text: string }) {
  return (
    <Typography variant="body2" color="text.secondary">
      {text || "—"}
    </Typography>
  );
}

/** Asks for the workspace's URL and an admin's API key. The key is held
 *  only while the dialog is open and cleared the moment it is sent, so
 *  neither a failure nor a re-render keeps it. */
function ConnectDialog({
  ws,
  onCancel,
  onConnected,
}: {
  ws: OutlineWorkspaceStatus;
  onCancel: () => void;
  onConnected: (keyUser: string, ignored: boolean) => void;
}) {
  const [url, setUrl] = useState(ws.url);
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | undefined>();
  const valid = /^https:\/\/\S+$/.test(url.trim()) && key.trim() !== "";
  const submit = async () => {
    const apiKey = key.trim();
    setKey("");
    setBusy(true);
    setFailure(undefined);
    try {
      const done = await outline.connectOutlineWorkspace({ workspace: ws.workspace, url: url.trim(), apiKey });
      onConnected(done.keyUser, done.keyUserIgnored);
    } catch (error) {
      setFailure(reason(error));
      setBusy(false);
    }
  };
  return (
    <Dialog open onClose={busy ? undefined : onCancel} fullWidth maxWidth="sm">
      <DialogTitle>{ws.connected ? `Replace the key of ${ws.workspace}` : `Connect ${ws.workspace}`}</DialogTitle>
      <DialogContent>
        <DialogContentText sx={{ mb: 2 }}>
          The origin Outline is served at, such as <Mono>https://docs.example.com</Mono>, and an API key of an Outline admin. The key is checked with Outline,
          kept where only the controller reads it, and never shown again.
          {ws.connected ? " A workspace connected before keeps its Outline team: the new key must belong to the same one." : ""}
        </DialogContentText>
        <Stack sx={{ gap: 2 }}>
          <TextField
            label="URL"
            placeholder="https://docs.example.com"
            fullWidth
            autoFocus={!ws.url}
            value={url}
            disabled={busy}
            onChange={(event) => setUrl(event.target.value)}
            slotProps={{ htmlInput: { spellCheck: false, autoCapitalize: "off" } }}
          />
          <TextField
            label="API key"
            type="password"
            autoComplete="off"
            fullWidth
            autoFocus={Boolean(ws.url)}
            value={key}
            disabled={busy}
            onChange={(event) => setKey(event.target.value)}
            slotProps={{ htmlInput: { spellCheck: false, autoCapitalize: "off", "data-lpignore": "true" } }}
          />
        </Stack>
        <Failure error={failure} />
      </DialogContent>
      <DialogActions>
        <Button onClick={onCancel} disabled={busy}>
          Cancel
        </Button>
        <Button variant="contained" disabled={busy || !valid} onClick={() => void submit()}>
          {ws.connected ? "Replace key" : "Connect"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}
