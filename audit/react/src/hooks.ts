import { create, toJsonString } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { ConnectError } from "@connectrpc/connect";
import { createContext, createElement, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";

import {
  FacetsRequestSchema,
  SearchRequestSchema,
  Sort_Field,
  Sentencer,
  Sort_Order,
  TimePredicateSchema,
  type Facet,
  type Filter,
  type GetResponse,
  type ProfileAccess,
  type QueryClient,
  type Record as AuditRecord,
  type Sentences,
} from "@truvity/audit";

interface Audit {
  client: QueryClient;
  sentencer: Sentencer;
}

const AuditContext = createContext<Audit | undefined>(undefined);

export interface AuditProviderProps {
  /** The query client, over the host's transport and credentials. */
  client: QueryClient;
  /** The application's catalogues; the common one is always included. */
  sentences?: Sentences[];
  locale?: string;
  children?: ReactNode;
}

/** Makes the client and the sentences available to the hooks and the view. */
export function AuditProvider({ client, sentences = [], locale = "en", children }: AuditProviderProps) {
  const sentencer = useMemo(() => new Sentencer(sentences, locale), [sentences, locale]);
  const value = useMemo(() => ({ client, sentencer }), [client, sentencer]);
  return createElement(AuditContext.Provider, { value }, children);
}

/** The client and sentences the nearest AuditProvider holds. */
export function useAudit(): Audit {
  const audit = useContext(AuditContext);
  if (!audit) throw new Error("@truvity/audit: wrap the view in an <AuditProvider>");
  return audit;
}

/** An error as a person can read it, with the service's code kept. */
export interface ReadError {
  code: string;
  message: string;
}

function readError(err: unknown): ReadError {
  const e = ConnectError.from(err);
  return { code: String(e.code), message: e.rawMessage || e.message };
}

// key is a stable identity for a request, so a hook refetches when what it
// asks changes and not when the caller builds an equal filter again.
function key(profile: string, filter: Filter | undefined): string {
  return profile + "\u0000" + toJsonString(SearchRequestSchema, create(SearchRequestSchema, {
    profile, filter: filter ? [filter] : [],
  }));
}

export interface Search {
  records: AuditRecord[];
  loading: boolean;
  error?: ReadError;
  /** Whether a further page may exist. */
  more: boolean;
  loadMore(): void;
  reload(): void;
}

/**
 * Pages through a profile, newest first. Changing the profile or the filter
 * starts again from the first page; loadMore follows the service's cursor.
 */
export function useSearch(profile: string, filter?: Filter, pageSize = 50): Search {
  const { client } = useAudit();
  const [records, setRecords] = useState<AuditRecord[]>([]);
  const [cursor, setCursor] = useState<string>("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<ReadError | undefined>();
  const [more, setMore] = useState(false);
  const [generation, setGeneration] = useState(0);
  const request = key(profile, filter);
  const current = useRef(request);

  const fetchPage = useCallback(
    async (from: string, replace: boolean) => {
      setLoading(true);
      setError(undefined);
      const asked = request;
      try {
        const page = await client.search({
          profile, filter: filter ? [filter] : [], limit: pageSize, cursor: from,
        });
        if (current.current !== asked) return;
        setRecords((have) => (replace ? page.items : [...have, ...page.items]));
        setCursor(page.cursors?.next ?? "");
        // A full page may be followed by another; a short one is the end for now.
        setMore(page.items.length >= pageSize && Boolean(page.cursors?.next));
      } catch (err) {
        if (current.current === asked) setError(readError(err));
      } finally {
        if (current.current === asked) setLoading(false);
      }
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [client, request, pageSize],
  );

  useEffect(() => {
    current.current = request;
    setRecords([]);
    setCursor("");
    void fetchPage("", true);
  }, [request, generation, fetchPage]);

  return {
    records,
    loading,
    ...(error ? { error } : {}),
    more,
    loadMore: () => {
      if (!loading && cursor) void fetchPage(cursor, false);
    },
    reload: () => setGeneration((g) => g + 1),
  };
}

export interface Tail {
  /** What was recorded since the tail started, newest first. */
  records: AuditRecord[];
  error?: ReadError;
}

/**
 * Follows what is recorded from now on. It asks in recorded order and keeps
 * following the service's `next` cursor, which on the last page yields what
 * was recorded since — so an event that occurred earlier but arrived late is
 * shown, not skipped. A searcher that cannot order by recorded time (the
 * archive scan) refuses; the error says so.
 */
export function useTail(profile: string, filter: Filter | undefined, enabled: boolean, everyMs = 5000): Tail {
  const { client } = useAudit();
  const [records, setRecords] = useState<AuditRecord[]>([]);
  const [error, setError] = useState<ReadError | undefined>();
  const request = key(profile, filter);

  useEffect(() => {
    setRecords([]);
    setError(undefined);
    if (!enabled) return;
    let stopped = false;
    let cursor = "";
    const since = create(TimePredicateSchema, {
      operator: { case: "greaterThanOrEqual", value: timestampFromDate(new Date()) },
    });
    const tailFilter = { ...(filter ?? {}), recordedAt: since } as Filter;
    const poll = async () => {
      try {
        const page = await client.search({
          profile,
          filter: [tailFilter],
          sort: [
            { field: Sort_Field.RECORDED_AT, order: Sort_Order.ASC },
            { field: Sort_Field.ID, order: Sort_Order.ASC },
          ],
          limit: 100,
          cursor,
        });
        if (stopped) return;
        cursor = page.cursors?.next ?? cursor;
        if (page.items.length > 0) {
          setRecords((have) => {
            const seen = new Set(have.map((r) => r.id));
            const fresh = page.items.filter((r) => !seen.has(r.id)).reverse();
            return [...fresh, ...have];
          });
        }
      } catch (err) {
        if (!stopped) setError(readError(err));
        stopped = true;
      }
    };
    void poll();
    const timer = setInterval(() => {
      if (!stopped) void poll();
    }, everyMs);
    return () => {
      stopped = true;
      clearInterval(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [client, request, enabled, everyMs]);

  return { records, ...(error ? { error } : {}) };
}

export interface Facets {
  facets: Facet[];
  error?: ReadError;
}

/** Counts the values of fields under the same filter, for navigation. */
export function useFacets(profile: string, filter: Filter | undefined, fields: string[], limit = 10): Facets {
  const { client } = useAudit();
  const [facets, setFacets] = useState<Facet[]>([]);
  const [error, setError] = useState<ReadError | undefined>();
  const request = key(profile, filter) + fields.join(",");

  useEffect(() => {
    let live = true;
    setError(undefined);
    if (fields.length === 0) {
      setFacets([]);
      return;
    }
    client
      .facets(create(FacetsRequestSchema, { profile, filter: filter ? [filter] : [], fields, limitPerField: limit }))
      .then((res) => live && setFacets(res.facets))
      .catch((err: unknown) => live && setError(readError(err)));
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [client, request, limit]);

  return { facets, ...(error ? { error } : {}) };
}

export interface RecordAt {
  response?: GetResponse;
  loading: boolean;
  error?: ReadError;
}

/** One record by id, with where it is kept and whether a digest covers it. */
export function useRecord(profile: string, id: string | undefined): RecordAt {
  const { client } = useAudit();
  const [response, setResponse] = useState<GetResponse | undefined>();
  const [loading, setLoading] = useState(Boolean(id));
  const [error, setError] = useState<ReadError | undefined>();

  useEffect(() => {
    let live = true;
    setResponse(undefined);
    setError(undefined);
    if (!id) {
      setLoading(false);
      return;
    }
    setLoading(true);
    client
      .get({ profile, id })
      .then((res) => live && setResponse(res))
      .catch((err: unknown) => live && setError(readError(err)))
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
  }, [client, profile, id]);

  return { ...(response ? { response } : {}), loading, ...(error ? { error } : {}) };
}

export interface Access {
  /** Each profile the caller may read, with what they may do on it. */
  profiles: ProfileAccess[];
  loading: boolean;
  error?: ReadError;
}

/**
 * What the caller may read, as the query service's grants say: the profiles
 * to offer, without the host having to know the deployment's names for them.
 */
export function useAccess(): Access {
  const { client } = useAudit();
  const [profiles, setProfiles] = useState<ProfileAccess[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<ReadError | undefined>();

  useEffect(() => {
    let live = true;
    setLoading(true);
    setError(undefined);
    client
      .access({})
      .then((res) => live && setProfiles(res.profiles))
      .catch((err: unknown) => live && setError(readError(err)))
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
  }, [client]);

  return { profiles, loading, ...(error ? { error } : {}) };
}
