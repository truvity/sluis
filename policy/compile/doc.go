// Package compile derives the policy's tables from an access model: who
// holds which internal group, for how long, and which directory groups,
// CI jobs and workloads admit a caller to it.
//
// An installation keeps its access model in a file of its own, in its own
// repository and in its own shape. It resolves that file into a [Model]
// (every named population replaced by the directory groups and addresses
// it stands for), states its own naming decisions in [Conventions], and
// calls [Roster] and [BuildVocabulary]. What comes back is the policy's
// `groups`, `lifetimes`, `github`, `slack` and `people` tables and its
// optional `vocabulary`, sorted, so a render does not churn.
//
// The package adds no concept to the policy. Every group it mints follows
// the one naming rule, `<scope>:<thing>:<role>` ([policy.SplitGroup]), and
// the two families that are deliberately not grants, `rung:<name>` and
// `emp:<slug>` ([policy.FamilyRung], [policy.FamilyEmp]).
//
// The model is layered the way the access file is:
//
//   - the rungs, the access ladder's populations and session lengths;
//   - the scopes matrix, rung by scope to a generic level, which the
//     cluster tier, the database levels and a project's viewer are read
//     from;
//   - business projects, their deploy and approve populations;
//   - application grants, a population handed one internal group outside
//     the ladder;
//   - the GitHub, Slack, CI job and workload rows, which are machines or
//     directory bindings rather than rungs.
//
// The validators beside the derivation ([ValidateGitHubJobs],
// [ValidateGitHubApps], [ValidateGitHubPullRequestGrants],
// [ValidateWorkloads], [Endpoints.Validate] and the
// rest) refuse at the installation's own render what the issuer would
// refuse at load, with a message that names the row.
package compile
