/** What whoami says about a module under maintenance, and the words for it.
 *
 *  The restore function sets the flag; every write the console would make to
 *  that module is refused until it is lifted, and reads keep working. */
export type Maintenance = {
  /** the module whose table carries the flag: oidc, github, slack, cloudflare, google or backup */
  module: string;
  state: string;
  since?: string;
  reason?: string;
};

const names: Record<string, string> = {
  oidc: "sign-in",
  github: "GitHub",
  slack: "Slack",
  cloudflare: "Cloudflare",
  google: "directories",
  backup: "backup",
};

/** A module's name as a person says it. */
export function moduleName(module: string): string {
  return names[module] ?? module;
}

/** The banner text, or undefined when nothing is under maintenance. */
export function maintenanceText(m?: Maintenance): string | undefined {
  if (!m || !m.state) return undefined;
  const what = names[m.module] ?? m.module;
  const since = m.since ? ` since ${m.since.replace("T", " ").replace("Z", " UTC")}` : "";
  const why = m.reason ? ` ${m.reason}.` : "";
  return `${what[0]!.toUpperCase()}${what.slice(1)} is under maintenance${since}: it is being ${m.state === "restoring" ? "restored from a backup" : m.state}, so changes are refused and the pages are read-only.${why}`;
}

/** How often the banner asks again, in milliseconds. */
export const maintenancePollMs = 30_000;
