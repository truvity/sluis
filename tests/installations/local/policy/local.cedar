// The policy of the local installation (tests/installations/local), written against the Cedar
// schema of sluis policy, version 1. The ids are stable: audit records and tests name them.

// ---- What a person may get a token for -------------------------------------------------

// A person who belongs to one of a client's required groups may have a token for it.
@id("token-requires")
permit (
  principal is Person,
  action in [Action::"token.issue", Action::"token.exchange"],
  resource is Client
)
when { principal in resource.requires };

// ---- Step-up ---------------------------------------------------------------------------

// `example-app` of the `dev` boundary wants multi-factor authentication no older than 15
// minutes. When a request carries no `amr` or no `auth_age`, as with an upstream that sends
// none, the answer is "not shown", so the person is asked to step up. The annotation tells
// `session` what to ask for (RFC 9470): the values of `acr_values` and `max_age` that it puts
// in `insufficient_user_authentication`.
@id("example-app-fresh-mfa")
@step_up("acr_values=urn:example:acr:mfa max_age=900")
forbid (
  principal is Person,
  action in [Action::"token.issue", Action::"token.exchange"],
  resource == Client::"dev/example-app"
)
unless {
  context has amr && context.amr.contains("mfa") &&
  context has auth_age && context.auth_age <= 900
};

// ---- Templates, for grants that operators link at run time -----------------------------

// Members of a group may have tokens for one client. Linked per tenant.
@id("grant-group-to-client")
permit (
  principal in ?principal,
  action in [Action::"token.issue", Action::"token.exchange"],
  resource == ?resource
);

// One person gets nothing. A forbid can only narrow, so it is what the console offers for
// "suspend".
@id("suspend-person")
forbid (principal == ?principal, action, resource);

// ---- Who may link which template (the delegation policy) -------------------------------

// A company's administrators may grant their own company's groups access to the clients that
// the company is offered.
@id("delegate-company-grants")
permit (
  principal in Group::"company-admins",
  action == Action::"policy.link",
  resource == Boundary::"dev"
)
when {
  context.template == "grant-group-to-client" &&
  context has principal_group && context.principal_group.org == principal.org &&
  context has resource_client && [Client::"dev/example-app"].contains(context.resource_client)
};

// The security group may suspend anyone.
@id("delegate-suspension")
permit (
  principal in Group::"security",
  action == Action::"policy.link",
  resource == Boundary::"dev"
)
when { context.template == "suspend-person" && context has principal_person };
