//! The grant vocabulary (docs/reference/policy.md "The grant vocabulary"): the grant names of a
//! boundary, their scopes, and the ladder of roles.
//!
//! The document is the config kind `GrantVocabulary`. This module reads it, checks what the schema
//! cannot state (policy.md "What the vocabulary checks"), and answers the two questions `policy`
//! asks of it: which grant names exist, and which roles a held role implies. An error names a path
//! of the document, never a value.

use std::collections::{BTreeMap, BTreeSet};

use crate::yaml::{self, Value};

/// The `$id` of the schema of schema v1 (policy.md "Format").
pub const SCHEMA_ID: &str = "https://sluis.id/schema/vocabulary/v1/vocabulary.schema.json";

/// A grant name in parts: `<scope>:<thing>:<role>`, or `<scope>:<vendor>:<project>:<customer>:<role>`, whose thing is
/// the three words between the scope and the role, joined by `:` (ADR 0055).
#[derive(Clone, Debug, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub struct GrantName {
    pub scope: String,
    pub thing: String,
    pub role: String,
}

impl GrantName {
    /// The grant name as text: the id of the Cedar entity `Grant`.
    #[must_use]
    pub fn id(&self) -> String {
        format!("{}:{}:{}", self.scope, self.thing, self.role)
    }

    /// Reads a grant name by its shape alone, declared or not (ADR 0055): three segments or five, each a valid name. The
    /// scope is the first segment, the role the last, and the thing is everything between them, joined by `:`, so it is one
    /// word or exactly three. Two segments, four, six or more, an empty segment, a wildcard and any character outside a
    /// name are not a grant name. `kits/conformance/token/grant-name-vectors.json` fixes the rule.
    #[must_use]
    pub fn split(text: &str) -> Option<Self> {
        let parts: Vec<&str> = text.split(SEPARATOR).collect();
        if !GRANT_SEGMENTS.contains(&parts.len()) || !parts.iter().all(|p| valid_name(p)) {
            return None;
        }
        let last = parts.len() - 1;
        Some(Self {
            scope: parts[0].to_owned(),
            thing: parts[1..last].join(SEPARATOR),
            role: parts[last].to_owned(),
        })
    }
}

/// What a grant name is written with between its segments.
pub const SEPARATOR: &str = ":";

/// The segment counts of a grant name: `<scope>:<thing>:<role>`, and the longer form whose thing is
/// `<vendor>:<project>:<customer>` (ADR 0055).
const GRANT_SEGMENTS: [usize; 2] = [3, 5];

/// The segment counts of a thing: one word, or the three of `<vendor>:<project>:<customer>`.
const THING_SEGMENTS: [usize; 2] = [1, 3];

/// Whether `s` can stand in the thing position of a grant name: one valid name, or exactly three joined by `:` (ADR 0055).
/// It is the thing [`GrantName::split`] reads back out of a name built from it.
#[must_use]
pub fn valid_thing(s: &str) -> bool {
    let parts: Vec<&str> = s.split(SEPARATOR).collect();
    THING_SEGMENTS.contains(&parts.len()) && parts.iter().all(|p| valid_name(p))
}

/// A document that is not a valid vocabulary: the path and the rule.
#[derive(Debug, PartialEq, Eq)]
pub struct VocabError(pub String);

impl std::fmt::Display for VocabError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for VocabError {}

fn err<T>(path: &str, rule: &str) -> Result<T, VocabError> {
    Err(VocabError(if path.is_empty() {
        rule.to_owned()
    } else {
        format!("{path}: {rule}")
    }))
}

#[derive(Debug)]
struct Role {
    implies: Vec<String>,
    scopes: BTreeSet<String>,
}

/// The vocabulary of one boundary.
#[derive(Debug)]
pub struct Vocabulary {
    /// Scope name to `sensitive`.
    scopes: BTreeMap<String, bool>,
    /// Thing name to role name to role.
    things: BTreeMap<String, BTreeMap<String, Role>>,
}

fn valid_name(s: &str) -> bool {
    (1..=64).contains(&s.len())
        && s.bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_')
}

fn map<'a>(v: &'a Value, path: &str) -> Result<&'a [(String, Value)], VocabError> {
    match v {
        Value::Map(m) => Ok(m),
        _ => err(path, "must be a mapping"),
    }
}

fn names(v: &Value, path: &str) -> Result<Vec<String>, VocabError> {
    let Value::Seq(items) = v else {
        return err(path, "must be a list of names");
    };
    items
        .iter()
        .enumerate()
        .map(|(i, item)| match item {
            Value::String(s) if valid_name(s) => Ok(s.clone()),
            _ => err(&format!("{path}[{i}]"), "not a valid name"),
        })
        .collect()
}

impl Vocabulary {
    /// Reads and checks the document.
    ///
    /// # Errors
    ///
    /// [`VocabError`] naming the path at fault.
    pub fn parse(document: &[u8]) -> Result<Self, VocabError> {
        let Ok(text) = std::str::from_utf8(document) else {
            return err("", "not UTF-8");
        };
        let root = yaml::parse(text).map_err(|e| VocabError(e.to_string()))?;
        let top = map(&root, "")?;
        let (mut scopes_v, mut things_v) = (None, None);
        for (k, v) in top {
            match k.as_str() {
                "$schema" => match v {
                    Value::String(s) if s == SCHEMA_ID => {}
                    _ => return err("$schema", "must name the schema of this version"),
                },
                "scopes" => scopes_v = Some(v),
                "things" => things_v = Some(v),
                _ => return err(k, "unknown key"),
            }
        }
        let Some(scopes_v) = scopes_v else {
            return err("scopes", "is required");
        };
        let Some(things_v) = things_v else {
            return err("things", "is required");
        };

        let mut scopes = BTreeMap::new();
        for (name, decl) in map(scopes_v, "scopes")? {
            let path = format!("scopes.{name}");
            if !valid_name(name) {
                return err(&path, "not a valid name");
            }
            let mut sensitive = false;
            for (k, v) in map(decl, &path)? {
                match (k.as_str(), v) {
                    ("sensitive", Value::Bool(b)) => sensitive = *b,
                    ("sensitive", _) => {
                        return err(&format!("{path}.sensitive"), "must be a boolean");
                    }
                    _ => return err(&format!("{path}.{k}"), "unknown key"),
                }
            }
            scopes.insert(name.clone(), sensitive);
        }
        if scopes.is_empty() {
            return err("scopes", "needs at least one scope");
        }

        let mut things = BTreeMap::new();
        for (name, decl) in map(things_v, "things")? {
            let path = format!("things.{name}");
            if !valid_thing(name) {
                return err(&path, "not a valid name");
            }
            let mut roles_v = None;
            for (k, v) in map(decl, &path)? {
                match k.as_str() {
                    "roles" => roles_v = Some(v),
                    _ => return err(&format!("{path}.{k}"), "unknown key"),
                }
            }
            let Some(roles_v) = roles_v else {
                return err(&format!("{path}.roles"), "is required");
            };
            let roles = Self::roles(roles_v, &format!("{path}.roles"), &scopes)?;
            things.insert(name.clone(), roles);
        }
        if things.is_empty() {
            return err("things", "needs at least one thing");
        }
        let v = Self { scopes, things };
        v.check_ladders()?;
        Ok(v)
    }

    fn roles(
        v: &Value,
        path: &str,
        scopes: &BTreeMap<String, bool>,
    ) -> Result<BTreeMap<String, Role>, VocabError> {
        let mut roles = BTreeMap::new();
        for (name, decl) in map(v, path)? {
            let rpath = format!("{path}.{name}");
            if !valid_name(name) {
                return err(&rpath, "not a valid name");
            }
            let mut role = Role {
                implies: Vec::new(),
                scopes: scopes.keys().cloned().collect(),
            };
            match decl {
                // The short form: the roles it implies.
                Value::Seq(_) => role.implies = names(decl, &rpath)?,
                // The long form.
                Value::Map(m) => {
                    for (k, v) in m {
                        let kpath = format!("{rpath}.{k}");
                        match k.as_str() {
                            "implies" => role.implies = names(v, &kpath)?,
                            "scopes" => {
                                let listed = names(v, &kpath)?;
                                if listed.is_empty() {
                                    return err(&kpath, "needs at least one scope");
                                }
                                let mut set = BTreeSet::new();
                                for (i, s) in listed.into_iter().enumerate() {
                                    if !scopes.contains_key(&s) {
                                        return err(
                                            &format!("{kpath}[{i}]"),
                                            "not a declared scope",
                                        );
                                    }
                                    if !set.insert(s) {
                                        return err(&format!("{kpath}[{i}]"), "repeated");
                                    }
                                }
                                role.scopes = set;
                            }
                            _ => return err(&kpath, "unknown key"),
                        }
                    }
                }
                _ => return err(&rpath, "must be a list of roles or a mapping"),
            }
            roles.insert(name.clone(), role);
        }
        if roles.is_empty() {
            return err(path, "needs at least one role");
        }
        Ok(roles)
    }

    /// policy.md "What the vocabulary checks": implied roles exist and are not the role itself,
    /// the ladder has no cycle, and a role exists in no scope an implied role lacks.
    fn check_ladders(&self) -> Result<(), VocabError> {
        for (thing, roles) in &self.things {
            for (name, role) in roles {
                let path = format!("things.{thing}.roles.{name}");
                for (i, implied) in role.implies.iter().enumerate() {
                    let ipath = format!("{path}.implies[{i}]");
                    if implied == name {
                        return err(&ipath, "a role cannot imply itself");
                    }
                    let Some(other) = roles.get(implied) else {
                        return err(&ipath, "not a role of this thing");
                    };
                    if !role.scopes.is_subset(&other.scopes) {
                        return err(&ipath, "the role exists in a scope the implied role lacks");
                    }
                }
                // A cycle: the role is reachable from itself.
                let mut seen = BTreeSet::new();
                let mut todo: Vec<&str> = role.implies.iter().map(String::as_str).collect();
                while let Some(r) = todo.pop() {
                    if r == name {
                        return err(&path, "the ladder has a cycle");
                    }
                    if seen.insert(r)
                        && let Some(next) = roles.get(r)
                    {
                        todo.extend(next.implies.iter().map(String::as_str));
                    }
                }
            }
        }
        Ok(())
    }

    /// Every grant name the document declares, with whether its scope is sensitive.
    #[must_use]
    pub fn grants(&self) -> Vec<(GrantName, bool)> {
        let mut out = Vec::new();
        for (thing, roles) in &self.things {
            for (role, r) in roles {
                for scope in &r.scopes {
                    out.push((
                        GrantName {
                            scope: scope.clone(),
                            thing: thing.clone(),
                            role: role.clone(),
                        },
                        self.scopes.get(scope).copied().unwrap_or(false),
                    ));
                }
            }
        }
        out
    }

    /// Whether the grant name is declared.
    #[must_use]
    pub fn declares(&self, g: &GrantName) -> bool {
        self.things
            .get(&g.thing)
            .and_then(|roles| roles.get(&g.role))
            .is_some_and(|r| r.scopes.contains(&g.scope))
    }

    /// Whether a scope is sensitive; an unknown scope is.
    #[must_use]
    pub fn sensitive(&self, scope: &str) -> bool {
        self.scopes.get(scope).copied().unwrap_or(true)
    }

    /// The grant names text (`dev:example-app:user`, or `devel:shared:keycloak:example-a:act-as`) as a name of the
    /// vocabulary, or `None` when it is not a declared grant name.
    #[must_use]
    pub fn parse_name(&self, text: &str) -> Option<GrantName> {
        GrantName::split(text).filter(|g| self.declares(g))
    }

    /// The held grant names with every role they imply added, by the ladder, on the same scope and
    /// thing (policy.md "How a person's grants are found", step 1).
    #[must_use]
    pub fn expand(&self, held: &BTreeSet<GrantName>) -> BTreeSet<GrantName> {
        let mut out = held.clone();
        let mut todo: Vec<GrantName> = held.iter().cloned().collect();
        while let Some(g) = todo.pop() {
            let Some(role) = self.things.get(&g.thing).and_then(|r| r.get(&g.role)) else {
                continue;
            };
            for implied in &role.implies {
                let next = GrantName {
                    scope: g.scope.clone(),
                    thing: g.thing.clone(),
                    role: implied.clone(),
                };
                if out.insert(next.clone()) {
                    todo.push(next);
                }
            }
        }
        out
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const LOCAL: &str =
        include_str!("../../../../tests/installations/local/policy/vocabulary.yaml");

    fn g(s: &str) -> GrantName {
        Vocabulary::parse(LOCAL.as_bytes())
            .unwrap()
            .parse_name(s)
            .unwrap()
    }

    #[test]
    fn the_local_vocabulary_declares_nine_grant_names() {
        let v = Vocabulary::parse(LOCAL.as_bytes()).unwrap();
        let all = v.grants();
        assert_eq!(all.len(), 9);
        assert!(
            all.iter()
                .filter(|(_, s)| *s)
                .all(|(g, _)| g.scope == "prod")
        );
        assert_eq!(all.iter().filter(|(_, s)| *s).count(), 3);
        assert!(v.parse_name("dev:example-app:user").is_some());
        assert!(v.parse_name("dev:example-app:root").is_none());
        assert!(v.parse_name("dev:example-app").is_none());
        assert!(v.parse_name("a:b:c:d").is_none());
        assert!(v.parse_name("dev:a:example-app:b:user").is_none());
    }

    #[test]
    fn the_ladder_is_expanded_on_the_same_scope_and_thing() {
        let v = Vocabulary::parse(LOCAL.as_bytes()).unwrap();
        let held = BTreeSet::from([g("acme:example-app:admin")]);
        let full: Vec<String> = v.expand(&held).iter().map(GrantName::id).collect();
        assert_eq!(
            full,
            [
                "acme:example-app:admin",
                "acme:example-app:operator",
                "acme:example-app:user"
            ]
        );
    }

    #[test]
    fn a_role_that_exists_in_some_scopes_only() {
        let v = Vocabulary::parse(
            b"scopes:\n  dev: {}\n  prod: { sensitive: true }\nthings:\n  deploy:\n    roles:\n      viewer: []\n      releaser: { implies: [viewer], scopes: [dev, prod] }\n      debugger: { implies: [viewer], scopes: [dev] }\n",
        )
        .unwrap();
        assert_eq!(v.grants().len(), 2 + 2 + 1);
        assert!(v.parse_name("prod:deploy:debugger").is_none());
        assert!(v.sensitive("prod") && !v.sensitive("dev"));
    }

    const GRANT_NAMES: &str = include_str!("../../../conformance/token/grant-name-vectors.json");

    /// ADR 0055: a grant name is three segments or five, and the thing is what lies between the scope and the role.
    ///
    /// verifies: REQ-POLICY-025
    #[test]
    fn a_grant_name_is_three_segments_or_five_by_the_shared_vectors() {
        let v: serde_json::Value = serde_json::from_str(GRANT_NAMES).unwrap();
        for case in v["grant_names"]["accepted"].as_array().unwrap() {
            let name = case["name"].as_str().unwrap();
            let g = GrantName::split(name).unwrap_or_else(|| panic!("refused {name}"));
            assert_eq!(g.scope, case["scope"].as_str().unwrap(), "{name}");
            assert_eq!(g.thing, case["thing"].as_str().unwrap(), "{name}");
            assert_eq!(g.role, case["role"].as_str().unwrap(), "{name}");
            assert_eq!(g.id(), name);
            assert!(valid_thing(&g.thing), "{name}");
        }
        for name in v["grant_names"]["refused"].as_array().unwrap() {
            let name = name.as_str().unwrap();
            assert_eq!(GrantName::split(name), None, "accepted {name:?}");
        }
        for t in v["things"]["accepted"].as_array().unwrap() {
            assert!(valid_thing(t.as_str().unwrap()), "{t}");
        }
        for t in v["things"]["refused"].as_array().unwrap() {
            assert!(!valid_thing(t.as_str().unwrap()), "{t}");
        }
    }

    /// ADR 0055: a thing of three words is declared, granted, laddered and parsed like a thing of one, beside it.
    ///
    /// verifies: REQ-POLICY-025
    #[test]
    fn a_vocabulary_declares_a_thing_of_three_words_beside_one_of_one() {
        let v = Vocabulary::parse(
            b"scopes:\n  devel: {}\n  prod: { sensitive: true }\nthings:\n  example-app:\n    roles:\n      user: []\n  shared:keycloak:example-a:\n    roles:\n      viewer: []\n      act-as: [viewer]\n",
        )
        .unwrap();
        assert_eq!(v.grants().len(), 2 + 2 * 2);
        let g = v
            .parse_name("devel:shared:keycloak:example-a:act-as")
            .unwrap();
        assert_eq!(
            (g.scope.as_str(), g.thing.as_str(), g.role.as_str()),
            ("devel", "shared:keycloak:example-a", "act-as")
        );
        let full: Vec<String> = v
            .expand(&BTreeSet::from([g]))
            .iter()
            .map(GrantName::id)
            .collect();
        assert_eq!(
            full,
            [
                "devel:shared:keycloak:example-a:act-as",
                "devel:shared:keycloak:example-a:viewer"
            ]
        );
        assert!(v.parse_name("devel:example-app:user").is_some());
        // A name of a shape that is not a grant name, or a thing that is not declared, is no name of the vocabulary.
        assert!(v.parse_name("devel:shared:keycloak:act-as").is_none());
        assert!(
            v.parse_name("devel:shared:keycloak:example-b:act-as")
                .is_none()
        );
        assert!(
            v.parse_name("devel:shared:keycloak:example-a:act-as:x")
                .is_none()
        );
        // A thing of two words, or of four, is refused by its path.
        for thing in [
            "shared:keycloak",
            "shared:keycloak:example-a:x",
            "shared::example-a",
        ] {
            assert_eq!(
                refused(&format!(
                    "scopes: {{dev: {{}}}}\nthings:\n  \"{thing}\":\n    roles: {{a: []}}\n"
                )),
                format!("things.{thing}: not a valid name")
            );
        }
    }

    fn refused(doc: &str) -> String {
        Vocabulary::parse(doc.as_bytes()).unwrap_err().0
    }

    #[test]
    fn what_the_vocabulary_checks_beyond_the_schema() {
        let head = "scopes: {dev: {}, prod: {sensitive: true}}\nthings:\n  t:\n    roles:\n";
        assert_eq!(
            refused(&format!("{head}      a: [a]\n")),
            "things.t.roles.a.implies[0]: a role cannot imply itself"
        );
        assert_eq!(
            refused(&format!("{head}      a: [nope]\n")),
            "things.t.roles.a.implies[0]: not a role of this thing"
        );
        assert_eq!(
            refused(&format!("{head}      a: [b]\n      b: [a]\n")),
            "things.t.roles.a: the ladder has a cycle"
        );
        assert_eq!(
            refused(&format!(
                "{head}      a: {{implies: [b]}}\n      b: {{scopes: [dev]}}\n"
            )),
            "things.t.roles.a.implies[0]: the role exists in a scope the implied role lacks"
        );
        assert_eq!(
            refused(&format!("{head}      a: {{scopes: [dev, dev]}}\n")),
            "things.t.roles.a.scopes[1]: repeated"
        );
        assert_eq!(
            refused(&format!("{head}      a: {{scopes: [qa]}}\n")),
            "things.t.roles.a.scopes[0]: not a declared scope"
        );
    }

    /// verifies: REQ-DATA-041
    ///
    /// The GrantVocabulary document is the vocabulary file itself, which store@ passes on and policy reads (the
    /// payload rules of store@ check its profile only): an unknown key at each of the four levels of the file is
    /// refused here, by its path, and the value it holds is not in the error.
    #[test]
    fn an_unknown_key_at_any_level_of_the_vocabulary_is_refused_and_its_value_is_not_shown() {
        let secret = "zq-secret-value-9b7d";
        for (doc, want) in [
            (
                format!("scopes: {{dev: {{}}}}\nthings: {{}}\nzq: {secret}\n"),
                "zq: unknown key",
            ),
            (
                format!("scopes: {{dev: {{zq: {secret}}}}}\nthings: {{}}\n"),
                "scopes.dev.zq: unknown key",
            ),
            (
                format!(
                    "scopes: {{dev: {{}}}}\nthings:\n  t:\n    zq: {secret}\n    roles: {{a: []}}\n"
                ),
                "things.t.zq: unknown key",
            ),
            (
                format!(
                    "scopes: {{dev: {{}}}}\nthings:\n  t:\n    roles:\n      a: {{zq: {secret}}}\n"
                ),
                "things.t.roles.a.zq: unknown key",
            ),
        ] {
            let e = refused(&doc);
            assert_eq!(e, want, "{doc}");
            assert!(!e.contains(secret), "{e}");
        }
    }

    #[test]
    fn shape_errors_name_a_path_and_never_a_value() {
        assert_eq!(refused("things: {}\n"), "scopes: is required");
        assert_eq!(
            refused("scopes: {}\nthings: {}\n"),
            "scopes: needs at least one scope"
        );
        assert_eq!(
            refused("scopes: {a b: {}}\nthings: {}\n"),
            "scopes.a b: not a valid name"
        );
        assert_eq!(
            refused("scopes: {dev: {}}\nthings: {}\nextra: 1\n"),
            "extra: unknown key"
        );
        assert_eq!(
            refused("scopes: {dev: {sensitive: yes-please}}\nthings: {}\n"),
            "scopes.dev.sensitive: must be a boolean"
        );
        assert_eq!(
            refused("$schema: https://example.com/other.json\nscopes: {}\nthings: {}\n"),
            "$schema: must name the schema of this version"
        );
    }
}
