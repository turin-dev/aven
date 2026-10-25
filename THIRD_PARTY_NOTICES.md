# Third-party notices

Aven (`aven-server`, `aven-agent`, the `aven` CLI and the embedded dashboard) is licensed under AGPL-3.0-only (see `LICENSE`). It includes the third-party software below, each under its own license. The full license texts are in the linked repositories; the SIL Open Font License text for the bundled fonts is reproduced at the end.

Regenerate this file with `make notices` when dependencies change.

## Go modules (compiled into the binaries)

| Module | License | Source |
| --- | --- | --- |
| `github.com/BurntSushi/toml` | MIT | https://github.com/BurntSushi/toml/blob/v1.6.0/COPYING |
| `github.com/beevik/etree` | BSD-2-Clause | https://github.com/beevik/etree/blob/v1.5.0/LICENSE |
| `github.com/coder/websocket` | ISC | https://github.com/coder/websocket/blob/v1.8.14/LICENSE.txt |
| `github.com/compose-spec/compose-go/v2` | Apache-2.0 | https://github.com/compose-spec/compose-go/blob/v2.16.1/LICENSE |
| `github.com/compose-spec/compose-go/v2/dotenv` | MIT | https://github.com/compose-spec/compose-go/blob/v2.16.1/dotenv/LICENSE |
| `github.com/crewjam/saml` | BSD-2-Clause | https://github.com/crewjam/saml/blob/v0.5.1/LICENSE |
| `github.com/distribution/reference` | Apache-2.0 | https://github.com/distribution/reference/blob/v0.5.0/LICENSE |
| `github.com/docker/go-connections/nat` | Apache-2.0 | https://github.com/docker/go-connections/blob/v0.4.0/LICENSE |
| `github.com/docker/go-units` | Apache-2.0 | https://github.com/docker/go-units/blob/v0.5.0/LICENSE |
| `github.com/dustin/go-humanize` | MIT | https://github.com/dustin/go-humanize/blob/v1.0.1/LICENSE |
| `github.com/fxamacker/cbor/v2` | MIT | https://github.com/fxamacker/cbor/blob/v2.9.2/LICENSE |
| `github.com/go-viper/mapstructure/v2` | MIT | https://github.com/go-viper/mapstructure/blob/v2.5.0/LICENSE |
| `github.com/go-webauthn/webauthn` | BSD-3-Clause | https://github.com/go-webauthn/webauthn/blob/v0.17.4/LICENSE |
| `github.com/go-webauthn/x/encoding/asn1` | BSD-3-Clause | https://github.com/go-webauthn/x/blob/v0.2.6/LICENSE |
| `github.com/go-webauthn/x/revoke` | BSD-2-Clause | https://github.com/go-webauthn/x/blob/v0.2.6/revoke/LICENSE |
| `github.com/golang-jwt/jwt/v4` | MIT | https://github.com/golang-jwt/jwt/blob/v4.5.2/LICENSE |
| `github.com/golang-jwt/jwt/v5` | MIT | https://github.com/golang-jwt/jwt/blob/v5.3.1/LICENSE |
| `github.com/google/go-tpm` | Apache-2.0 | https://github.com/google/go-tpm/blob/v0.9.8/LICENSE |
| `github.com/google/uuid` | BSD-3-Clause | https://github.com/google/uuid/blob/v1.6.0/LICENSE |
| `github.com/graphql-go/graphql` | MIT | https://github.com/graphql-go/graphql/blob/v0.8.1/LICENSE |
| `github.com/jonboulle/clockwork` | Apache-2.0 | https://github.com/jonboulle/clockwork/blob/v0.2.2/LICENSE |
| `github.com/klauspost/compress` | Apache-2.0 | https://github.com/klauspost/compress/blob/v1.20.3/LICENSE |
| `github.com/klauspost/compress` | BSD-3-Clause | https://github.com/klauspost/compress/blob/v1.20.3/LICENSE |
| `github.com/klauspost/compress` | MIT | https://github.com/klauspost/compress/blob/v1.20.3/LICENSE |
| `github.com/klauspost/compress/internal/snapref` | BSD-3-Clause | https://github.com/klauspost/compress/blob/v1.20.3/internal/snapref/LICENSE |
| `github.com/klauspost/compress/zstd/internal/xxhash` | MIT | https://github.com/klauspost/compress/blob/v1.20.3/zstd/internal/xxhash/LICENSE.txt |
| `github.com/mattermost/xml-roundtrip-validator` | Apache-2.0 | https://github.com/mattermost/xml-roundtrip-validator/blob/v0.1.0/LICENSE.txt |
| `github.com/mattn/go-shellwords` | MIT | https://github.com/mattn/go-shellwords/blob/v1.0.12/LICENSE |
| `github.com/opencontainers/go-digest` | Apache-2.0 | https://github.com/opencontainers/go-digest/blob/v1.0.0/LICENSE |
| `github.com/philhofer/fwd` | MIT | https://github.com/philhofer/fwd/blob/v1.2.0/LICENSE.md |
| `github.com/remyoudompheng/bigfft` | BSD-3-Clause | https://github.com/remyoudompheng/bigfft/blob/24d4a6f8daec/LICENSE |
| `github.com/russellhaering/goxmldsig` | Apache-2.0 | https://github.com/russellhaering/goxmldsig/blob/v1.4.0/LICENSE |
| `github.com/santhosh-tekuri/jsonschema/v6` | Apache-2.0 | https://github.com/santhosh-tekuri/jsonschema/blob/v6.0.1/LICENSE |
| `github.com/sirupsen/logrus` | MIT | https://github.com/sirupsen/logrus/blob/v1.10.1/LICENSE |
| `github.com/tinylib/msgp/msgp` | MIT | https://github.com/tinylib/msgp/blob/v1.6.4/LICENSE |
| `github.com/x448/float16` | MIT | https://github.com/x448/float16/blob/v0.8.4/LICENSE |
| `github.com/xhit/go-str2duration/v2` | BSD-3-Clause | https://github.com/xhit/go-str2duration/blob/v2.1.0/LICENSE |
| `go.yaml.in/yaml/v4` | Apache-2.0 | https://github.com/yaml/go-yaml/blob/v4.0.0-rc.4/LICENSE |
| `golang.org/x/crypto` | BSD-3-Clause | https://cs.opensource.google/go/x/crypto/+/v0.58.0:LICENSE |
| `golang.org/x/sync` | BSD-3-Clause | https://cs.opensource.google/go/x/sync/+/v0.24.0:LICENSE |
| `golang.org/x/sys` | BSD-3-Clause | https://cs.opensource.google/go/x/sys/+/v0.49.0:LICENSE |
| `golang.org/x/text` | BSD-3-Clause | https://cs.opensource.google/go/x/text/+/v0.43.0:LICENSE |
| `modernc.org/libc` | BSD-3-Clause | https://gitlab.com/cznic/libc/-/blob/v1.77.1/LICENSE |
| `modernc.org/mathutil` | BSD-3-Clause | https://gitlab.com/cznic/mathutil/-/blob/v1.7.1/LICENSE |
| `modernc.org/memory` | BSD-3-Clause | https://gitlab.com/cznic/memory/-/blob/v1.12.1/LICENSE |
| `modernc.org/sqlite` | BSD-3-Clause | https://gitlab.com/cznic/sqlite/-/blob/v1.60.1/LICENSE |

## JavaScript packages (bundled into the dashboard)

| Package | License | Source |
| --- | --- | --- |
| `@fontsource-variable/inter@5.3.0` | OFL-1.1 | https://github.com/fontsource/font-files |
| `@fontsource-variable/jetbrains-mono@5.3.0` | OFL-1.1 | https://github.com/fontsource/font-files |
| `@img/colour@1.1.0` | MIT | https://github.com/lovell/colour |
| `@img/sharp-libvips-linux-x64@1.3.4` | LGPL-3.0-or-later | https://github.com/lovell/sharp-libvips |
| `@img/sharp-linux-x64@0.35.5` | Apache-2.0 | https://github.com/lovell/sharp |
| `@next/env@16.4.2` | MIT | https://github.com/vercel/next.js |
| `@next/swc-linux-x64-gnu@16.4.2` | MIT | https://github.com/vercel/next.js |
| `@swc/helpers@0.5.23` | Apache-2.0 | https://github.com/swc-project/swc |
| `@tanstack/query-core@5.104.3` | MIT | https://github.com/TanStack/query |
| `@tanstack/react-query@5.104.3` | MIT | https://github.com/TanStack/query |
| `@types/react@19.3.0` | MIT | https://github.com/DefinitelyTyped/DefinitelyTyped |
| `baseline-browser-mapping@2.11.29` | Apache-2.0 | https://github.com/web-platform-dx/baseline-browser-mapping |
| `caniuse-lite@1.0.30001816` | CC-BY-4.0 | https://github.com/browserslist/caniuse-lite |
| `client-only@0.0.1` | MIT |  |
| `csstype@3.2.3` | MIT | https://github.com/frenic/csstype |
| `detect-libc@2.1.2` | Apache-2.0 | https://github.com/lovell/detect-libc |
| `lucide-react@1.58.0` | ISC | https://github.com/lucide-icons/lucide |
| `nanoid@3.3.20` | MIT | https://github.com/ai/nanoid |
| `next@16.4.2` | MIT | https://github.com/vercel/next.js |
| `picocolors@1.1.1` | ISC | https://github.com/alexeyraspopov/picocolors |
| `postcss@8.5.23` | MIT | https://github.com/postcss/postcss |
| `pretendard@1.3.9` | OFL-1.1 | https://github.com/orioncactus/pretendard |
| `react-dom@19.3.1` | MIT | https://github.com/react/react |
| `react@19.3.1` | MIT | https://github.com/react/react |
| `scheduler@0.28.0` | MIT | https://github.com/react/react |
| `semver@7.8.5` | ISC | https://github.com/npm/node-semver |
| `sharp@0.35.5` | Apache-2.0 | https://github.com/lovell/sharp |
| `simple-icons@16.35.0` | CC0-1.0 | https://github.com/simple-icons/simple-icons |
| `source-map-js@1.2.2` | BSD-3-Clause | https://github.com/7rulnik/source-map-js |
| `styled-jsx@5.1.6` | MIT | https://github.com/vercel/styled-jsx |
| `tslib@2.8.1` | 0BSD | https://github.com/Microsoft/tslib |

Build-time only (not shipped): `caniuse-lite` (CC-BY-4.0) through browserslist, and `sharp`/libvips (LGPL-3.0-or-later), which Next.js installs but the static export does not use.

## Fonts

Inter, JetBrains Mono and Pretendard are bundled through `@fontsource-variable/inter`, `@fontsource-variable/jetbrains-mono` and `pretendard` under the SIL Open Font License 1.1.

- Copyright 2016 The Inter Project Authors (https://github.com/rsms/inter) Inter-Italic[opsz,wght].ttf: Copyright 2016 The Inter Project Authors (https://github.com/rsms/inter)
- Copyright 2020 The JetBrains Mono Project Authors (https://github.com/JetBrains/JetBrainsMono) JetBrainsMono-Italic[wght].ttf: Copyright 2020 The JetBrains Mono Project Authors (https://github.com/JetBrains/JetBrainsMono)
- Copyright (c) 2021, Kil Hyung-jin (https://github.com/orioncactus/pretendard),
- with Reserved Font Name Pretendard.

```text
This Font Software is licensed under the SIL Open Font License, Version 1.1.
This license is copied below, and is also available with a FAQ at:
http://scripts.sil.org/OFL


-----------------------------------------------------------
SIL OPEN FONT LICENSE Version 1.1 - 26 February 2007
-----------------------------------------------------------

PREAMBLE
The goals of the Open Font License (OFL) are to stimulate worldwide
development of collaborative font projects, to support the font creation
efforts of academic and linguistic communities, and to provide a free and
open framework in which fonts may be shared and improved in partnership
with others.

The OFL allows the licensed fonts to be used, studied, modified and
redistributed freely as long as they are not sold by themselves. The
fonts, including any derivative works, can be bundled, embedded,
redistributed and/or sold with any software provided that any reserved
names are not used by derivative works. The fonts and derivatives,
however, cannot be released under any other type of license. The
requirement for fonts to remain under this license does not apply
to any document created using the fonts or their derivatives.

DEFINITIONS
"Font Software" refers to the set of files released by the Copyright
Holder(s) under this license and clearly marked as such. This may
include source files, build scripts and documentation.

"Reserved Font Name" refers to any names specified as such after the
copyright statement(s).

"Original Version" refers to the collection of Font Software components as
distributed by the Copyright Holder(s).

"Modified Version" refers to any derivative made by adding to, deleting,
or substituting -- in part or in whole -- any of the components of the
Original Version, by changing formats or by porting the Font Software to a
new environment.

"Author" refers to any designer, engineer, programmer, technical
writer or other person who contributed to the Font Software.

PERMISSION & CONDITIONS
Permission is hereby granted, free of charge, to any person obtaining
a copy of the Font Software, to use, study, copy, merge, embed, modify,
redistribute, and sell modified and unmodified copies of the Font
Software, subject to the following conditions:

1) Neither the Font Software nor any of its individual components,
in Original or Modified Versions, may be sold by itself.

2) Original or Modified Versions of the Font Software may be bundled,
redistributed and/or sold with any software, provided that each copy
contains the above copyright notice and this license. These can be
included either as stand-alone text files, human-readable headers or
in the appropriate machine-readable metadata fields within text or
binary files as long as those fields can be easily viewed by the user.

3) No Modified Version of the Font Software may use the Reserved Font
Name(s) unless explicit written permission is granted by the corresponding
Copyright Holder. This restriction only applies to the primary font name as
presented to the users.

4) The name(s) of the Copyright Holder(s) or the Author(s) of the Font
Software shall not be used to promote, endorse or advertise any
Modified Version, except to acknowledge the contribution(s) of the
Copyright Holder(s) and the Author(s) or with their explicit written
permission.

5) The Font Software, modified or unmodified, in part or in whole,
must be distributed entirely under this license, and must not be
distributed under any other license. The requirement for fonts to
remain under this license does not apply to any document created
using the Font Software.

TERMINATION
This license becomes null and void if any of the above conditions are
not met.

DISCLAIMER
THE FONT SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO ANY WARRANTIES OF
MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT
OF COPYRIGHT, PATENT, TRADEMARK, OR OTHER RIGHT. IN NO EVENT SHALL THE
COPYRIGHT HOLDER BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY,
INCLUDING ANY GENERAL, SPECIAL, INDIRECT, INCIDENTAL, OR CONSEQUENTIAL
DAMAGES, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING
FROM, OUT OF THE USE OR INABILITY TO USE THE FONT SOFTWARE OR FROM
OTHER DEALINGS IN THE FONT SOFTWARE.
```
