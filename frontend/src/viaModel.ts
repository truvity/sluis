import { paths } from "./router";

/** Where a "through ..." reason of a holder leads. A holder is in a group
 *  through a provider group (an address, which has a page of its own) or
 *  through a matcher, which the policy describes in words ("signed in at
 *  tsarev.id", "signed in as ann@tsarev.id", "CI job with ..."). Only the
 *  first kind is a provider group: linking a matcher's words to the
 *  provider group page asks the hub for a group of that name and gets
 *  "address has no domain". A domain leads to the people of that domain,
 *  an address to its person, and the rest to nothing. */
export function viaTarget(why: string): string | undefined {
  const domain = /^signed in at (\S+)$/.exec(why);
  if (domain) return paths.peopleAtDomain(domain[1]);
  const person = /^signed in as (\S+)$/.exec(why);
  if (person) return paths.person(person[1]);
  if (/^(CI job with|ServiceAccount|AWS role|nothing)( |$)/.test(why)) return undefined;
  return paths.directoryGroup(why);
}
