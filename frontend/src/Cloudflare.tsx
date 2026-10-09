import { useState } from "react";
import Alert from "@mui/material/Alert";
import Box from "@mui/material/Box";
import Button from "@mui/material/Button";
import Dialog from "@mui/material/Dialog";
import DialogActions from "@mui/material/DialogActions";
import DialogContent from "@mui/material/DialogContent";
import DialogContentText from "@mui/material/DialogContentText";
import DialogTitle from "@mui/material/DialogTitle";
import IconButton from "@mui/material/IconButton";
import Paper from "@mui/material/Paper";
import Stack from "@mui/material/Stack";
import Table from "@mui/material/Table";
import TableBody from "@mui/material/TableBody";
import TableCell from "@mui/material/TableCell";
import TableContainer from "@mui/material/TableContainer";
import TableHead from "@mui/material/TableHead";
import TableRow from "@mui/material/TableRow";
import Tooltip from "@mui/material/Tooltip";
import Typography from "@mui/material/Typography";
import ContentCopyIcon from "@mui/icons-material/ContentCopy";

import { ago, at, cloudflare, reason, until, type Me } from "./api";
import type {
  CloudflareLiveToken,
  CloudflarePreset,
  GetCloudflareCredentialResponse,
  MyCloudflarePreset,
} from "./gen/directoryroster/v1/cloudflare_pb";
import { awsProfile, credentialProcess, freshness, freshnessNote, prototypeView, span, tokenCommand } from "./cloudflareModel";
import { useAsync } from "./hooks";
import { ConfirmDialog, Failure, Loading, Mono, Nothing, Page, Section, State } from "./ui";

type Props = { me?: Me; operator: boolean; onDone: (message: string) => void };

/** Cloudflare, as sluis serves it (docs/guides/sluis/cloudflare-tokens.md): what a
 *  person is granted and may ask a token for, and, for whoever may read the
 *  installation, the accounts, the presets, the token each keeps and the ones
 *  minted on demand.
 *
 *  No value of a kept token is ever on this page. The one credential it
 *  shows is the answer to "Get a token": minted for the person, held in this
 *  component's state until the dialog closes, and written nowhere. */
export function CloudflarePage({ me, operator, onDone }: Props) {
  // A person with no role at all can still be granted a preset, so the
  // administrator's listing is asked for only by whoever may read it: the
  // server would refuse the rest, and a refusal is not what the page is for.
  const admin = (me?.roles ?? []).includes("viewer");
  return (
    <Page
      title="Cloudflare"
      lede="sluis mints short-lived Cloudflare API tokens and R2 credentials from a prototype token per preset. A token is cloned from the prototype's rights, expires on its own and is named for who it was made for."
    >
      <Mine />
      {admin ? <Administration operator={operator} onDone={onDone} /> : null}
    </Page>
  );
}

// ------------------------------------------------------------- the person

function Mine() {
  const mine = useAsync(() => cloudflare.listMyCloudflarePresets({}), []);
  const presets = mine.value?.presets ?? [];
  const [asking, setAsking] = useState<MyCloudflarePreset | undefined>();
  return (
    <Section title="Your presets" hint="The presets the policy grants you through your groups.">
      <Loading busy={mine.loading} />
      <Failure error={mine.error} />
      {mine.value && !mine.value.available ? <Nothing>This deployment mints no Cloudflare credentials.</Nothing> : null}
      {mine.value?.available && presets.length === 0 ? (
        <Nothing>No preset is granted to you. A grant names an internal group (or a CI job) in the policy&rsquo;s cloudflare.grants.</Nothing>
      ) : null}
      {presets.length > 0 ? (
        <TableContainer component={Paper} variant="outlined" sx={{ overflowX: "auto" }}>
          <Table size="small" sx={{ minWidth: 560 }}>
            <TableHead>
              <TableRow>
                <TableCell>Preset</TableCell>
                <TableCell>Kind</TableCell>
                <TableCell>Longest lifetime</TableCell>
                <TableCell align="right" />
              </TableRow>
            </TableHead>
            <TableBody>
              {presets.map((preset) => (
                <TableRow key={preset.name} hover>
                  <TableCell>
                    <Mono>{preset.name}</Mono>
                    <Typography variant="caption" color="text.secondary" sx={{ display: "block" }}>
                      {preset.description}
                    </Typography>
                  </TableCell>
                  <TableCell>{preset.endpoint ? "R2 credentials" : "API token"}</TableCell>
                  <TableCell>{span(Number(preset.lifetimeSeconds))}</TableCell>
                  <TableCell align="right">
                    <Button size="small" variant="contained" onClick={() => setAsking(preset)}>
                      {preset.endpoint ? "Get credentials" : "Get a token"}
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      ) : null}
      {presets.length > 0 ? <Hints presets={presets} /> : null}
      {asking ? <CredentialDialog key={asking.name} preset={asking} onClose={() => setAsking(undefined)} /> : null}
    </Section>
  );
}

/** The command line is the better way to hold a credential: it renews on its
 *  own and the console is not in the loop. */
function Hints({ presets }: { presets: MyCloudflarePreset[] }) {
  const r2 = presets.filter((p) => p.endpoint);
  const tokens = presets.filter((p) => !p.endpoint);
  return (
    <Box sx={{ mt: 2 }}>
      {tokens.length > 0 ? (
        <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
          From a terminal, a token is <Mono>{tokenCommand(tokens[0].name)}</Mono>; it is cached and renewed when a third of its life is left.
        </Typography>
      ) : null}
      {r2.length > 0 ? (
        <Typography variant="body2" color="text.secondary">
          R2 credentials belong in an AWS profile that runs <Mono>{credentialProcess(r2[0].name)}</Mono>. <Mono>sluisctl aws-config</Mono> writes a profile for every preset you are granted.
        </Typography>
      ) : null}
    </Box>
  );
}

/** Asks for a credential, shows it once, and forgets it when closed.
 *
 *  Nothing here reaches storage: the response lives in this component's
 *  state, the dialog unmounts with it, and the copy button writes to the
 *  clipboard only when pressed. A reload asks for another. */
function CredentialDialog({ preset, onClose }: { preset: MyCloudflarePreset; onClose: () => void }) {
  const [got, setGot] = useState<GetCloudflareCredentialResponse | undefined>();
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | undefined>();
  const r2 = Boolean(preset.endpoint);

  const mint = async () => {
    setBusy(true);
    setFailure(undefined);
    try {
      setGot(await cloudflare.getCloudflareCredential({ preset: preset.name }));
    } catch (error) {
      setFailure(reason(error));
    } finally {
      setBusy(false);
    }
  };

  const close = () => {
    setGot(undefined);
    onClose();
  };

  return (
    <Dialog open onClose={busy ? undefined : close} fullWidth maxWidth="md">
      <DialogTitle>{r2 ? `R2 credentials for ${preset.name}` : `A token for ${preset.name}`}</DialogTitle>
      <DialogContent>
        {!got ? (
          <DialogContentText sx={{ mb: 2 }}>
            sluis makes a {r2 ? "credential" : "token"} for you with the rights of the preset&rsquo;s prototype. It lives for up to {span(Number(preset.lifetimeSeconds))} and is named for you in
            Cloudflare and in the audit trail. It is shown once, here, and is not kept anywhere.
          </DialogContentText>
        ) : (
          <>
            <Alert severity="warning" sx={{ mb: 2 }}>
              This is shown once. Copy it now; closing this dialog forgets it, and a new one means minting another.
            </Alert>
            {r2 ? (
              <Stack sx={{ gap: 1.5 }}>
                <Secret label="Access key id" value={got.accessKeyId} />
                <Secret label="Secret access key" value={got.secretAccessKey} />
                <Secret label="Endpoint" value={got.endpoint} />
                <Typography variant="body2" color="text.secondary">
                  Or let the command line hold it and renew it: put this in <Mono>~/.aws/config</Mono>, or run <Mono>sluisctl aws-config</Mono>.
                </Typography>
                <Secret label="AWS profile" value={awsProfile(preset.name, got.endpoint)} multiline />
              </Stack>
            ) : (
              <Secret label="Token" value={got.token} />
            )}
            <Typography variant="caption" color="text.secondary" sx={{ display: "block", mt: 1.5 }}>
              id <Mono>{got.tokenId}</Mono> · expires {until(at(got.expiresOn))}
            </Typography>
          </>
        )}
        <Failure error={failure} />
      </DialogContent>
      <DialogActions>
        <Button onClick={close} disabled={busy}>
          {got ? "Done" : "Cancel"}
        </Button>
        {!got ? (
          <Button variant="contained" disabled={busy} onClick={() => void mint()}>
            {r2 ? "Get credentials" : "Get a token"}
          </Button>
        ) : null}
      </DialogActions>
    </Dialog>
  );
}

/** A value with a copy button. It is a read-only field rather than text in
 *  the page, so it is not part of what a screen reader reads aloud or what a
 *  select-all copies by accident. */
function Secret({ label, value, multiline }: { label: string; value: string; multiline?: boolean }) {
  const [copied, setCopied] = useState(false);
  const copy = () => {
    void navigator.clipboard
      ?.writeText(value)
      .then(() => {
        setCopied(true);
        setTimeout(() => setCopied(false), 2000);
      })
      .catch(() => undefined);
  };
  return (
    <Box>
      <Typography variant="caption" color="text.secondary">
        {label}
      </Typography>
      <Stack direction="row" sx={{ alignItems: multiline ? "flex-start" : "center", gap: 1 }}>
        <Box
          component={multiline ? "pre" : "div"}
          sx={{ m: 0, flexGrow: 1, minWidth: 0, p: 1, borderRadius: 1, bgcolor: "action.hover", fontFamily: "monospace", fontSize: "0.8rem", wordBreak: "break-all", whiteSpace: multiline ? "pre-wrap" : "normal" }}
        >
          {value}
        </Box>
        <Tooltip title={copied ? "Copied" : "Copy"}>
          <IconButton size="small" aria-label={`copy ${label}`} onClick={copy}>
            <ContentCopyIcon fontSize="small" />
          </IconButton>
        </Tooltip>
      </Stack>
    </Box>
  );
}

// ------------------------------------------------------ the administrator

type Confirming =
  | { kind: "rotate"; preset: CloudflarePreset }
  | { kind: "revoke"; preset: CloudflarePreset; token: Pick<CloudflareLiveToken, "id" | "caller" | "stored"> };

function Administration({ operator, onDone }: { operator: boolean; onDone: (message: string) => void }) {
  const listed = useAsync(() => cloudflare.listCloudflare({}), []);
  const [confirming, setConfirming] = useState<Confirming | undefined>();
  const accounts = listed.value?.accounts ?? [];
  const presets = listed.value?.presets ?? [];
  const canOperate = operator && Boolean(listed.value?.canOperate);

  return (
    <>
      <Section
        title="Accounts"
        hint="The Cloudflare accounts sluis holds a minter credential for. Ids are shown by their last four characters."
        action={
          <Button size="small" onClick={listed.reload} disabled={listed.loading}>
            Refresh
          </Button>
        }
      >
        <Loading busy={listed.loading} />
        <Failure error={listed.error} />
        {listed.value && !listed.value.available ? <Nothing>This deployment has no cloudflare section in its service document.</Nothing> : null}
        {accounts.length > 0 ? (
          <Stack direction="row" sx={{ gap: 3, flexWrap: "wrap" }}>
            {accounts.map((a) => (
              <Typography key={a.name} variant="body2">
                <Mono>{a.name}</Mono> <Typography component="span" variant="caption" color="text.secondary">account …{a.idLast4}</Typography>
              </Typography>
            ))}
          </Stack>
        ) : null}
      </Section>

      {listed.value?.available ? (
        <Section
          title="Presets"
          hint="Each preset is a prototype token (disabled) plus how long its tokens live and how often the stored one is replaced. The prototype is read from Cloudflare each time this page opens."
        >
          {presets.length === 0 ? <Nothing>No preset is declared.</Nothing> : null}
          {presets.map((preset) => (
            <PresetCard key={preset.name} preset={preset} canOperate={canOperate} onRotate={() => setConfirming({ kind: "rotate", preset })} onRevoke={(token) => setConfirming({ kind: "revoke", preset, token })} />
          ))}
        </Section>
      ) : null}

      {confirming ? (
        <ConfirmPreset
          key={confirming.kind + confirming.preset.name}
          confirming={confirming}
          onCancel={() => setConfirming(undefined)}
          onDone={(message) => {
            setConfirming(undefined);
            onDone(message);
            listed.reload();
          }}
        />
      ) : null}
    </>
  );
}

function PresetCard({
  preset,
  canOperate,
  onRotate,
  onRevoke,
}: {
  preset: CloudflarePreset;
  canOperate: boolean;
  onRotate: () => void;
  onRevoke: (token: Pick<CloudflareLiveToken, "id" | "caller" | "stored">) => void;
}) {
  const proto = prototypeView(preset.prototype);
  const stored = preset.stored;
  const fresh = freshness(preset, stored, new Date());
  const note = freshnessNote(fresh, preset.rotationSeconds);
  return (
    <Paper variant="outlined" sx={{ p: 2, mb: 2 }}>
      <Stack direction="row" sx={{ justifyContent: "space-between", alignItems: "flex-start", gap: 2, flexWrap: "wrap" }}>
        <Box sx={{ minWidth: 0 }}>
          <Typography variant="subtitle2">
            <Mono>{preset.name}</Mono> <Typography component="span" variant="caption" color="text.secondary">{preset.endpoint ? "R2 credentials" : "API token"} · account {preset.account}</Typography>
          </Typography>
          <Typography variant="body2" color="text.secondary">
            {preset.description}
          </Typography>
        </Box>
        {canOperate ? (
          <Tooltip title={proto.usable ? "Mint the stored token now instead of at its next rotation" : "The prototype is refused: nothing can be minted until it is fixed"}>
            <span>
              <Button size="small" variant="outlined" disabled={!proto.usable} onClick={onRotate}>
                Rotate now
              </Button>
            </span>
          </Tooltip>
        ) : null}
      </Stack>

      <Stack direction="row" sx={{ gap: 4, flexWrap: "wrap", mt: 1.5 }}>
        <Fact label="Lifetime">{span(Number(preset.lifetimeSeconds))}</Fact>
        <Fact label="Rotation">{span(Number(preset.rotationSeconds))}</Fact>
        {preset.endpoint ? (
          <Fact label="Endpoint">
            <Mono>{preset.endpoint}</Mono>
          </Fact>
        ) : null}
        <Fact label="Prototype">
          <Mono>{preset.prototype?.id}</Mono> <State kind={proto.kind} label={proto.label} title={proto.title} />
        </Fact>
      </Stack>
      {!proto.usable && preset.prototype?.detail ? (
        <Alert severity="warning" sx={{ mt: 1.5 }}>
          {preset.prototype.detail}
        </Alert>
      ) : null}

      <Typography variant="caption" color="text.secondary" sx={{ display: "block", mt: 2, mb: 0.5 }}>
        Stored token (what consumers read)
      </Typography>
      {stored?.error ? (
        <Failure error={stored.error} />
      ) : stored?.present ? (
        <Stack direction="row" sx={{ gap: 3, flexWrap: "wrap", alignItems: "center" }}>
          <Typography variant="body2">
            id <Mono>{stored.tokenId || "—"}</Mono>
          </Typography>
          <Typography variant="body2">minted {ago(at(stored.mintedAt))}</Typography>
          <Typography variant="body2">expires {until(at(stored.expiresOn))}</Typography>
          {fresh === "stale" ? <State kind="failing" label="not rotating" title={note} /> : null}
          {fresh === "late" ? <State kind="pending" label="past rotation" title={note} /> : null}
          {canOperate && stored.tokenId ? (
            <Button size="small" color="warning" onClick={() => onRevoke({ id: stored.tokenId, caller: "", stored: true })}>
              Revoke
            </Button>
          ) : null}
        </Stack>
      ) : (
        <Typography variant="body2" color="text.secondary">
          {note}
        </Typography>
      )}
      {note && fresh !== "none" && fresh !== "fresh" ? (
        <Alert severity={fresh === "stale" ? "error" : "warning"} sx={{ mt: 1 }}>
          {note}
        </Alert>
      ) : null}

      <Typography variant="caption" color="text.secondary" sx={{ display: "block", mt: 2, mb: 0.5 }}>
        Tokens minted on demand that are still live
      </Typography>
      <Failure error={preset.liveError} />
      {preset.live.length === 0 && !preset.liveError ? (
        <Typography variant="body2" color="text.secondary">
          None.
        </Typography>
      ) : (
        <TableContainer sx={{ overflowX: "auto" }}>
          <Table size="small">
            <TableBody>
              {preset.live.map((token) => (
                <TableRow key={token.id}>
                  <TableCell>
                    <Mono>{token.caller || "(older stored token)"}</Mono>
                  </TableCell>
                  <TableCell>
                    <Mono>{token.id}</Mono>
                  </TableCell>
                  <TableCell>minted {ago(at(token.mintedAt))}</TableCell>
                  <TableCell>expires {until(at(token.expiresOn))}</TableCell>
                  <TableCell align="right">
                    {canOperate ? (
                      <Button size="small" color="warning" onClick={() => onRevoke(token)}>
                        Revoke
                      </Button>
                    ) : null}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}
    </Paper>
  );
}

function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <Box sx={{ minWidth: 0 }}>
      <Typography variant="caption" color="text.secondary" sx={{ display: "block" }}>
        {label}
      </Typography>
      <Typography component="div" variant="body2" sx={{ display: "flex", alignItems: "center", gap: 0.75, minHeight: 24 }}>
        {children}
      </Typography>
    </Box>
  );
}

/** Rotate and revoke act on a credential consumers depend on, so each asks
 *  once, says what follows, and names who is accountable: the audit trail
 *  records the signed-in operator for both. */
function ConfirmPreset({ confirming, onCancel, onDone }: { confirming: Confirming; onCancel: () => void; onDone: (message: string) => void }) {
  const { preset } = confirming;

  const run = async () => {
    if (confirming.kind === "rotate") {
      await cloudflare.rotateCloudflarePreset({ preset: preset.name });
      onDone(`${preset.name} was rotated: a new stored token is in place.`);
    } else {
      const res = await cloudflare.revokeCloudflareToken({ preset: preset.name, tokenId: confirming.token.id });
      onDone(res.replaced ? `The stored token of ${preset.name} was revoked and a new one is in place.` : `A token of ${preset.name} was revoked.`);
    }
  };

  return (
    <ConfirmDialog
      title={confirming.kind === "rotate" ? `Rotate ${preset.name} now?` : `Revoke a token of ${preset.name}?`}
      confirm={confirming.kind === "rotate" ? "Rotate now" : "Revoke"}
      danger={confirming.kind === "revoke"}
      run={run}
      onCancel={onCancel}
    >
      {confirming.kind === "rotate" ? (
        <>
          sluis mints a new stored token and writes it where consumers read it. The one it replaces stays valid until it expires, so a consumer that has it keeps working. This is
          recorded in the audit trail under your name.
        </>
      ) : confirming.token.stored ? (
        <>
          This deletes the stored token <Mono>{confirming.token.id}</Mono> in Cloudflare at once and mints its replacement, so consumers that already hold it fail and the next read
          gets the new one. Recorded in the audit trail under your name.
        </>
      ) : (
        <>
          This deletes <Mono>{confirming.token.id}</Mono>, minted for <Mono>{confirming.token.caller}</Mono>, in Cloudflare at once. Whatever uses it fails from now on. Recorded in the
          audit trail under your name.
        </>
      )}
    </ConfirmDialog>
  );
}
