# Python Package Index (PyPI)

The `pypi` registry type implements the [Python Simple Repository API](https://packaging.python.org/en/latest/specifications/simple-repository-api/),
so `pip`, `uv`, `poetry` and other Python installers can resolve packages through the
mirror instead of reaching `pypi.org` directly.

It also implements the [legacy upload API](https://docs.pypi.org/api/upload/), so `twine`
can publish packages to it.

---

## Quick start

```bash
# 1. Point pip at the mirror.
pip install --index-url http://localhost/r/python-packages/simple/ requests

# 2. Or configure it once for a virtual environment.
pip config set global.index-url http://localhost/r/python-packages/simple/
```

With `uv`:

```bash
uv pip install --index-url http://localhost/r/python-packages/simple/ requests
```

The default seeded instance is `python-packages` (slug), created on first startup when
`SEED_PYPI=true` (the default). It runs in **hybrid** mode: it mirrors `pypi.org` and
also accepts local publishing. Change that with `DEFAULT_PYPI_MODE=proxy|hosted|hybrid`.

---

## Protocol

### Simple Repository API (PEP 503 / PEP 691)

| Request | Response |
|---|---|
| `GET /r/<slug>/simple/` | Project index — every project name |
| `GET /r/<slug>/simple/<project>/` | One project's distribution files |
| `GET /r/<slug>/packages/<project>/<version>/<filename>` | The distribution file itself |
| `GET /r/<slug>/packages/<project>/<version>/<filename>.metadata` | The PEP 658 core metadata |

Project names are **normalized** per the packaging name-normalization specification:
runs of `-`, `_` and `.` collapse to a single `-`, and the result is lowercased. So
`Zope.Interface`, `zope-interface` and `zope_interface` are the same project.

```bash
curl -s http://localhost/r/python-packages/simple/requests/
# <!DOCTYPE html>
# <html>
#   <head>
#     <meta name="pypi:repository-version" content="1.1">
#     <title>Links for requests</title>
#   </head>
#   <body>
#     <h1>Links for requests</h1>
#     <a href="http://localhost/r/python-packages/packages/requests/2.31.0/requests-2.31.0-py3-none-any.whl"
#        data-requires-python="&gt;=3.7"
#        data-dist-info-metadata="sha256=...">requests-2.31.0-py3-none-any.whl</a><br>
#   </body>
# </html>
```

### Content negotiation (PEP 691)

The `Accept` header selects the serialization:

| `Accept` | `Content-Type` |
|---|---|
| `application/vnd.pypi.simple.v1+json` | `application/vnd.pypi.simple.v1+json` |
| `application/vnd.pypi.simple.latest+json` | `application/vnd.pypi.simple.v1+json` |
| `application/vnd.pypi.simple.v1+html` | `application/vnd.pypi.simple.v1+html` |
| `application/json` | `application/vnd.pypi.simple.v1+json` |
| `text/html`, `*/*`, absent | `text/html; charset=utf-8` |
| anything else | **406 Not Acceptable** |

```bash
curl -s -H 'Accept: application/vnd.pypi.simple.v1+json' \
  http://localhost/r/python-packages/simple/requests/
# {
#   "meta": { "api-version": "1.1" },
#   "name": "requests",
#   "files": [
#     {
#       "filename": "requests-2.31.0-py3-none-any.whl",
#       "url": "http://localhost/r/python-packages/packages/requests/2.31.0/requests-2.31.0-py3-none-any.whl",
#       "hashes": { "sha256": "..." },
#       "requires-python": ">=3.7",
#       "yanked": false,
#       "core-metadata": "sha256=...",
#       "size": 12345,
#       "upload-time": "2023-05-22T12:00:00Z"
#     }
#   ],
#   "versions": ["2.31.0"]
# }
```

Error responses use [RFC 9457](https://www.rfc-editor.org/rfc/rfc9457) problem details:

```bash
curl -s -H 'Accept: application/vnd.pypi.simple.v1+json' \
  http://localhost/r/python-packages/simple/does-not-exist/
# {
#   "type": "about:blank",
#   "title": "Not Found",
#   "status": 404,
#   "detail": "Unknown project."
# }
```

### Core metadata (PEP 658 / PEP 714)

When a distribution carries core metadata, the index advertises its hash and the
metadata file itself is served next to the distribution:

```
.../packages/requests/2.31.0/requests-2.31.0-py3-none-any.whl
.../packages/requests/2.31.0/requests-2.31.0-py3-none-any.whl.metadata
```

Installers use the sidecar to resolve dependencies without downloading the whole
distribution. Set `serve_core_metadata=false` to turn this off.

### Versions (PEP 440)

Python versions are **not** semver. The registry implements PEP 440 ordering
directly, including epochs, pre-releases, post-releases, development releases and
local versions:

```
1.0.dev1 < 1.0a1 < 1.0b1 < 1.0rc1 < 1.0 < 1.0.post1 < 1.1
1.0 < 1.0+local          (a local version sorts above its base version)
1!1.0 > 2.0              (an explicit epoch sorts above every non-epoch version)
```

Pre-releases are excluded by default. Set `include_prerelease=true` to serve them.

### Warehouse-style JSON API

For tooling that expects the classic PyPI JSON shape:

```bash
curl -s http://localhost/r/python-packages/pypi/requests/json
```

---

## Publishing

The registry implements the legacy upload API that `twine` and `uv` use.

```bash
# 1. Create an access token with the "publish" scope in the Admin Console.
# 2. Upload with twine.
TWINE_USERNAME=__token__ \
TWINE_PASSWORD=mr_your_token_here \
twine upload --repository-url http://localhost/r/python-packages/legacy/ dist/*
```

`twine` authenticates with **HTTP Basic**. The username is conventionally `__token__`
and the password carries the API token; the platform accepts that scheme natively
alongside its own `Authorization: Bearer` header.

The upload endpoint accepts the standard multipart fields — `:action=file_upload`,
`protocol_version`, `content`, `name`, `version`, `filetype`, `pyversion` and the
digest fields (`sha256_digest`, `md5_digest`, `blake2_256_digest`).

On upload the registry:

1. verifies the supplied digest against the uploaded bytes;
2. inspects the wheel or source distribution and extracts its core metadata;
3. stores the distribution file and its `.metadata` sidecar;
4. records the project, release and file records.

Wheels and source distributions (`.tar.gz`, `.zip`, `.tgz`, `.tar.bz2`, `.tar.xz`)
are both supported.

---

## Configuration

| Key | Type | Default | Description |
|---|---|---|---|
| `upstream_timeout_seconds` | number | `60` | Timeout for upstream index and file requests (1–600s). |
| `max_file_size_bytes` | number | `104857600` | Maximum size of a single distribution file (100 MiB). |
| `include_prerelease` | boolean | `false` | Serve alpha/beta/rc/dev releases to clients that do not opt in. |
| `require_upload_hash` | boolean | `true` | Reject uploads that carry no digest. |
| `serve_core_metadata` | boolean | `true` | Serve the PEP 658 `.metadata` sidecars. |
| `allowed_projects` | string list | *(empty)* | Allow-list of project name patterns. |
| `blocked_projects` | string list | *(empty)* | Deny-list of project name patterns. |
| `protected_projects` | string list | *(empty)* | Never mirrored from upstream in hybrid mode. |

Patterns support exact names, a trailing `*` wildcard, or `*` for everything.

### Repository modes

| Mode | Mirrors upstream | Accepts uploads |
|---|---|---|
| `proxy` | ✅ | ❌ |
| `hosted` | ❌ | ✅ |
| `hybrid` | ✅ | ✅ |

In **hybrid** mode a project listed in `protected_projects` is always served from the
local store, never from upstream.

---

## Environment variables

| Variable | Default | Description |
|---|---|---|
| `SEED_PYPI` | `true` | Seed the default PyPI instance on first startup. |
| `DEFAULT_PYPI_SLUG` | `python-packages` | Slug of the seeded instance. |
| `DEFAULT_PYPI_NAME` | `Python Package Index` | Display name of the seeded instance. |
| `DEFAULT_PYPI_UPSTREAM_URL` | `https://pypi.org` | Upstream index for proxy and hybrid modes. |
| `DEFAULT_PYPI_MODE` | `hybrid` | Repository mode of the seeded instance. |

---

## Storage

PyPI metadata is persisted in three MongoDB collections:

| Collection | Contents |
|---|---|
| `pypi_projects` | One document per project. |
| `pypi_releases` | One document per project + version. |
| `pypi_files` | One document per distribution file. |

The split reflects Python's model: a release carries **many** files (one source
distribution plus any number of platform wheels), unlike ecosystems where one version
maps to exactly one artifact.

---

## Root-level compatibility

When `ENABLE_ROOT_COMPAT_ROUTE=true` (the default) and the PyPI registry is a
default instance, the index is also reachable at the server root:

```
/simple/          -> /r/python-packages/simple/
/legacy/          -> /r/python-packages/legacy/
```

This lets tools configured with a bare `--index-url http://localhost/simple/` work
without knowing the instance slug.
