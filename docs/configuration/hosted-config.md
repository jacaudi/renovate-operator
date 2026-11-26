# Hosted Renovate Config

The operator can serve Renovate presets itself, so repositories can `extends` a
shared default (and any number of supplemental presets) without a separate config
repository or static file host.

You write each preset as YAML. The operator converts it to JSON and serves it under
three names, all with the same JSON body:

```
https://<operator-host>/config/<name>.json
https://<operator-host>/config/<name>.jsonc
https://<operator-host>/config/<name>.json5
```

JSON is valid JSONC and JSON5, so use whichever extension you prefer in `extends`.
The YAML source itself is not served.

This is different from [`spec.renovateConfig`](./renovate-config.md), which mounts
the *global* (self-hosted) config file into a `RenovateJob`'s pods. Hosted config is
for presets that repository configs, or that global config, reference by URL.

## Enabling

Write the presets in the Helm values, keyed by name. The chart generates a ConfigMap
with one `<name>.yaml` key per preset:

```yaml
hostedConfig:
  enabled: true
  files:
    default:
      extends:
        - config:recommended
        - https://renovate.example.com/config/automerge.json
      timezone: Europe/Berlin
    automerge:
      packageRules:
        - matchUpdateTypes: [patch]
          automerge: true
```

A name starts with a letter or digit and otherwise uses only letters, digits, `_`
and `-`.

Or point at ConfigMaps you manage yourself, in the operator's namespace, whose keys
are YAML files (`default.yaml`, `automerge.yaml`). Each can hold any number of files:

```yaml
hostedConfig:
  enabled: true
  existingConfigMaps:
    - team-a-presets
    - org-presets
```

All listed ConfigMaps, and the one generated from `files` if you set both, are merged
into a single folder, so a file is served at `/config/<name>.json` whichever
ConfigMap it comes from. **File names must be unique across them.** Kubernetes does
not report a clash: the ConfigMap listed later wins, and `files` wins over all.
So **list the ConfigMap you trust most last**, or keep org-wide presets in `files`:
anyone who can edit a later ConfigMap can replace a preset from an earlier one.

A listed ConfigMap that does not exist keeps the operator pod from starting. If one is
deleted while the operator runs, the mounted presets stop updating until it is back.

Only `.yaml` keys are read.
Plain JSON is almost always valid YAML, so an existing JSON preset can usually be
pasted into a `.yaml` key as it is. The exceptions return a 500: JSON5 or JSONC with
comments, the `\/` escape, and `\uD83D\uDE00`-style surrogate pairs.

The ConfigMaps are mounted into the operator. A file is converted the first time it is
requested and again only when its content changes. An edit is
served as soon as Kubernetes refreshes the mount, typically within a minute; the
operator does not restart.

## Using the presets

Reference a file by its full URL:

```json
{
  "extends": [
    "https://renovate.example.com/config/default.json"
  ]
}
```

When a [base path](./base-path.md) is configured it is part of the URL:
`https://example.com/renovate/config/default.json`.

## Things to know

- **The route is public.** Renovate fetches presets without a browser session, so
  the preset URLs are exempt from the UI login whenever hosted config is enabled, and
  anyone who can reach the operator's UI host can read the files. Never put
  credentials in a preset.
- **Private addresses need a grant.** Renovate warns about, and can be set to block,
  preset fetches from loopback and private-range hosts
  ([`internalHostAccess`](https://docs.renovatebot.com/self-hosted-configuration/#internalhostaccess)).
  If the operator's host resolves to such an address, allow it in the global config
  (for example through `spec.renovateConfig`). These options are recent additions to
  Renovate; check the linked page for the Renovate version your jobs run:

  ```json
  {
    "hostRules": [
      {
        "hostType": "preset",
        "matchHost": "https://renovate.example.com/config/",
        "allowInternal": true
      }
    ]
  }
  ```

- **Quote anything that looks like a boolean or a number.** The conversion follows
  YAML's typing rules, not what you meant: unquoted `n`, `y`, `yes`, `no`, `on` and
  `off` become `true`/`false` (as values and as keys, including preset names under
  `files`), `1.10` becomes `1.1`, and `010` becomes `8`. Write `"no"`, `"1.10"`.
- **A repeated key keeps its last value.** YAML with the same key twice in one mapping
  is not an error; the later value is served.
- **One document, at most 256 KiB.** Only the first YAML document in a file is
  served; anything after a `---` separator is ignored. A larger source returns a 500.
- **Only the YAML syntax is checked.** A source that is not valid YAML, or whose top
  level is not a mapping, returns a 500 and is logged by the operator. Whether the
  content is a valid Renovate config is still up to Renovate, and shows up in the job
  log. YAML comments are not carried into the JSON.
- **Nothing is applied automatically.** A `RenovateJob` only uses a hosted preset if
  its global config or a repository config `extends` it.

## Outside the Helm chart

The operator serves the directory named by the `HOSTED_CONFIG_DIR` environment
variable; the feature is off when it is empty. Every `.yaml` file directly in that
directory becomes public as JSON, so point it at a directory that holds
nothing else.
