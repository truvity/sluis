/** What the Sessions page makes of the issuer's listing: names a person can
 *  read, sessions grouped so a dozen identical connector sign-ins are one row,
 *  and the flag for sign-ins nobody ever used. No React here, so each rule is
 *  tested on its own. */

type Stamp = { seconds: bigint; nanos: number };

/** The part of a Session this model reads. The generated type satisfies it. */
export interface SessionFacts {
    id: string;
    identity: string;
    clientId: string;
    /** Empty from an issuer that predates the field. */
    clientName: string;
    /** Empty when the client itself is the audience, and for older sessions. */
    resource: string;
    resourceName: string;
    scopes: string[];
    issuedAt?: Stamp;
    lastRefreshed?: Stamp;
}

/** A sign-in that was never refreshed is only suspicious once the client has
 *  had time to use it: a connector redeems its first token within seconds. */
export const NEVER_USED_AFTER_MS = 5 * 60 * 1000;

const ms = (stamp?: Stamp): number | undefined =>
    stamp ? Number(stamp.seconds) * 1000 + stamp.nanos / 1e6 : undefined;

function hostOf(raw: string): string {
    try {
        return new URL(raw).host;
    } catch {
        return "";
    }
}

/** The client as a person reads it: the issuer's name for it, else the host
 *  of its URL, else the id. The raw id stays available for a tooltip. */
export function clientLabel(s: Pick<SessionFacts, "clientId" | "clientName">): string {
    return s.clientName || hostOf(s.clientId) || s.clientId;
}

/** The resource as a person reads it: the policy's name, else the host.
 *  "unknown" when none was recorded -- which is also what a client that named
 *  no resource looks like, so the page says what it can and no more. */
export function resourceLabel(s: Pick<SessionFacts, "resource" | "resourceName">): string {
    if (!s.resource) return "unknown";
    return s.resourceName || hostOf(s.resource) || s.resource;
}

/** Whether a session was opened more than a few minutes ago and has never
 *  been refreshed: most likely a connector sign-in that was abandoned. */
export function neverUsed(
    s: Pick<SessionFacts, "issuedAt" | "lastRefreshed">,
    now: number = Date.now(),
): boolean {
    if (s.lastRefreshed) return false;
    const opened = ms(s.issuedAt);
    return opened !== undefined && now - opened > NEVER_USED_AFTER_MS;
}

/** Whether a "client contains" filter matches what the row shows. The issuer
 *  matches the same four things; this is for rows already in hand. */
export function matchesClient(s: SessionFacts, needle: string): boolean {
    const n = needle.trim().toLowerCase();
    if (!n) return true;
    return [s.clientId, clientLabel(s), s.resource, resourceLabel(s)].some((text) =>
        text.toLowerCase().includes(n),
    );
}

export interface SessionGroup<T extends SessionFacts> {
    key: string;
    clientId: string;
    clientName: string;
    resource: string;
    resourceName: string;
    /** Newest first. */
    sessions: T[];
    /** How many of them were never used. */
    unused: number;
    /** The latest refresh among them; undefined when none was ever used. */
    lastUsed?: number;
}

export interface PersonSessions<T extends SessionFacts> {
    identity: string;
    total: number;
    groups: SessionGroup<T>[];
}

/** Person, then client, then resource. Identical sessions -- the same person
 *  on the same client for the same resource -- are one group. Order follows
 *  the listing (newest first), so the most recent person and group lead. */
export function groupSessions<T extends SessionFacts>(
    sessions: T[],
    now: number = Date.now(),
): PersonSessions<T>[] {
    const people = new Map<string, PersonSessions<T>>();
    for (const s of sessions) {
        let person = people.get(s.identity);
        if (!person) {
            person = { identity: s.identity, total: 0, groups: [] };
            people.set(s.identity, person);
        }
        person.total++;
        const key = `${s.clientId}\u0000${s.resource}`;
        let group = person.groups.find((g) => g.key === key);
        if (!group) {
            group = {
                key,
                clientId: s.clientId,
                clientName: s.clientName,
                resource: s.resource,
                resourceName: s.resourceName,
                sessions: [],
                unused: 0,
            };
            person.groups.push(group);
        }
        group.sessions.push(s);
        if (neverUsed(s, now)) group.unused++;
        const used = ms(s.lastRefreshed);
        if (used !== undefined && (group.lastUsed === undefined || used > group.lastUsed)) {
            group.lastUsed = used;
        }
    }
    return [...people.values()];
}
