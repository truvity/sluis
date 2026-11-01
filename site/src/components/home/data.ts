// What the home page says, as data. Every claim about a vendor is one that a page of these
// docs states: the GitHub and Slack ceremonies and Google delegation on
// /integrations/connecting/, the Cloudflare minting token on /integrations/cloudflare/,
// the AWS provider limit on /integrations/connecting/#aws-oidc-provider-and-role-trust.

export interface Chip {
  name: string;
  /** A name in the sprite (marks.ts, or a glyph in Marks.astro). */
  mark: string;
  /** What is hard, in one short sentence. */
  note: string;
  /** Whether the mark is a brand mark, as opposed to a plain glyph. */
  brand?: boolean;
}

// Pain 1: every system has its own place to say who may come in.
export const fragmented: Chip[] = [
  { name: 'AWS', mark: 'aws', brand: true, note: 'An identity provider and a trust policy in every account.' },
  { name: 'Google Cloud', mark: 'googlecloud', brand: true, note: 'Its own project, its own consent screen.' },
  { name: 'GitHub', mark: 'github', brand: true, note: 'Single sign-on per organisation. CI uses Apps and tokens instead.' },
  { name: 'GitLab', mark: 'gitlab', brand: true, note: 'A second git host, a second sign-in to set up.' },
  { name: 'Slack', mark: 'slack', brand: true, note: 'Sign-in is one setting. Bots and apps are another.' },
  { name: 'CI jobs', mark: 'githubactions', brand: true, note: 'Secrets copied into each pipeline.' },
  { name: 'PostgreSQL', mark: 'postgresql', brand: true, note: 'Roles and passwords inside each database.' },
  { name: 'Kubernetes', mark: 'kubernetes', brand: true, note: 'OIDC settings on every API server.' },
  { name: 'SSH hosts', mark: 'terminal', note: 'authorized_keys on every host.' },
  { name: 'Argo CD', mark: 'argo', brand: true, note: 'A login of its own, with groups mapped by hand.' },
  { name: 'Grafana', mark: 'grafana', brand: true, note: 'Another login, another group mapping.' },
  { name: 'Team wiki', mark: 'page', note: 'Accounts kept apart from the directory.' },
];

// Pain 2: the setup that should be code is a ceremony in a web console.
export const ceremonies: Chip[] = [
  { name: 'GitHub App', mark: 'github', brand: true, note: 'Created in a web form that an owner confirms. There is no API to change its permissions later.' },
  { name: 'Slack app', mark: 'slack', brand: true, note: 'No one-click flow. A configuration token lasts 12 hours, and a new scope means a reinstall.' },
  { name: 'Cloudflare', mark: 'cloudflare', brand: true, note: 'No STS: no native way to trade an identity for a short-lived, scoped API token. The token that makes tokens is created by hand.' },
  { name: 'Google Workspace', mark: 'google', brand: true, note: 'Domain-wide delegation is granted by a Super Admin in the console, by client id.' },
  { name: 'Google OAuth consent', mark: 'googlecloud', brand: true, note: 'In testing mode, refresh tokens expire after seven days.' },
  { name: 'AWS IAM', mark: 'aws', brand: true, note: 'One OIDC provider per account and a trust policy per role. At most 100 providers per account.' },
  { name: 'Team wiki', mark: 'page', note: 'No service accounts: the API key belongs to a person.' },
  { name: 'On-call tool', mark: 'bell', note: 'A key that an administrator creates by hand.' },
  { name: 'Service-account keys', mark: 'key', note: 'Long-lived keys to rotate, or forbidden by policy.' },
  { name: 'The SSO plan', mark: 'tag', note: 'Single sign-on is often sold on a higher plan.' },
  { name: 'Console settings', mark: 'drift', note: 'Set by hand, so they drift from what was reviewed.' },
];

export interface Link {
  text: string;
  href: string;
}

// What sluis does about each pain, and where to read it.
export const answers = {
  sso: {
    heading: 'One sign-in, trusted everywhere',
    text: 'People sign in through the identity provider that already knows them, such as Google Workspace or any OpenID Connect provider. sluis issues the tokens, and AWS, Kubernetes, PostgreSQL, SSH and your applications each get one short trust entry for sluis instead of a login of their own.',
    links: [
      { text: 'Integrations', href: '/integrations/' },
      { text: 'Google Workspace', href: '/integrations/google-workspace/' },
      { text: 'Generic OIDC', href: '/integrations/oidc-upstream/' },
      { text: 'AWS', href: '/integrations/aws/' },
      { text: 'Kubernetes', href: '/integrations/kubernetes/' },
      { text: 'PostgreSQL', href: '/integrations/postgresql/' },
      { text: 'SSH', href: '/integrations/ssh/' },
      { text: 'Applications', href: '/integrations/applications/' },
    ] satisfies Link[],
  },
  code: {
    heading: 'Declared in a reviewed file, checked for drift',
    text: 'Each integration is declared as data. sluis runs the vendor ceremony once, keeps what comes back sealed, compares what was granted with what was declared, and does nothing until the integration is enabled. Where a vendor has no STS, sluis plays that part: a job trades its identity for a token that lasts minutes and reaches one repository, one zone or one bucket.',
    links: [
      { text: 'Connecting integrations', href: '/integrations/connecting/' },
      { text: 'GitHub', href: '/integrations/github/' },
      { text: 'Cloudflare', href: '/integrations/cloudflare/' },
      { text: 'Slack and more', href: '/integrations/more/' },
      { text: 'The local installation', href: '/runtimes/local/' },
    ] satisfies Link[],
  },
};

// The six properties of sluis's side of the chain, each with the page that owns the fact.
export const secure: { name: string; href: string }[] = [
  { name: 'Intermediates only', href: '/principles/#what-sluis-is-and-is-not' },
  { name: 'Keys born in their store', href: '/principles/#1-a-key-is-born-in-its-store-and-never-leaves-it' },
  { name: 'No tenant keys', href: '/principles/#3-keys-are-shared-rotated-and-given-to-tenants-by-rule' },
  { name: 'A signed audit trail', href: '/concepts/audit/' },
  { name: 'Short-lived credentials', href: '/concepts/identity/' },
  { name: 'Verify, never trust', href: '/principles/#4-verify-never-trust-a-claim' },
];

// The eleven principles of docs/principles.md, in its order, with who shares each.
export const principles: { name: string; href: string; who: 'shared' | 'part' | 'sluis' }[] = [
  { name: 'A key is born in its store and never leaves it', href: '#1-a-key-is-born-in-its-store-and-never-leaves-it', who: 'shared' },
  { name: 'Trust is layered, and redundancy is not a defence against compromise', href: '#2-trust-is-layered-and-redundancy-is-not-a-defence-against-compromise', who: 'shared' },
  { name: 'Keys are shared, rotated and given to tenants by rule', href: '#3-keys-are-shared-rotated-and-given-to-tenants-by-rule', who: 'part' },
  { name: 'Verify; never trust a claim', href: '#4-verify-never-trust-a-claim', who: 'shared' },
  { name: 'Open standards, as their consumers implement them', href: '#5-open-standards-as-their-consumers-implement-them', who: 'part' },
  { name: 'Generic products, joined by a contract', href: '#6-generic-products-joined-by-a-contract', who: 'shared' },
  { name: 'A person only where a person adds safety', href: '#7-a-person-only-where-a-person-adds-safety', who: 'shared' },
  { name: 'Nothing without a consumer', href: '#8-nothing-without-a-consumer', who: 'shared' },
  { name: 'Measure before deciding, and date the measurement', href: '#9-measure-before-deciding-and-date-the-measurement', who: 'shared' },
  { name: 'One product, many shapes', href: '#10-one-product-many-shapes', who: 'sluis' },
  { name: 'Public by default, private where it protects', href: '#11-public-by-default-private-where-it-protects', who: 'shared' },
];
