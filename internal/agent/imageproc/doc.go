// Package imageproc validates and compresses user-uploaded images before
// they are inlined as vision `image_url` content parts or materialized
// into the session workspace.
//
// Rationale (upstream issue #106): today every ingress path (web chat,
// OpenAI-compatible API, IM channels) forwards uploaded bytes verbatim.
// That means (a) a declared "image/png" that is actually an SVG / AVIF /
// arbitrary bytes reaches the provider's vision channel and sinks the
// whole turn with an upstream 400, and (b) a 12-megapixel photo is sent
// to the model at full resolution, burning tokens and latency for no
// quality gain — providers downscale internally anyway.
//
// The pipeline is three stages:
//
//  1. Sniff (sniff.go): magic-byte detection. The sniffed format always
//     wins over the declared MIME — clients lie, bytes don't. Sniffing
//     and header-dimension reads are dependency-free and never decode
//     pixels.
//  2. Policy (policy.go): a canonical allowlist (PNG / JPEG / GIF /
//     WebP). Anything else — unknown bytes, mismatched declarations,
//     decompression-bomb headers — is Downgraded: the file is still
//     stored in /workspace (the agent can inspect it with tools) but it
//     must never reach an `image_url` content part.
//  3. Compress (compress.go): accepted images that exceed the edge or
//     byte budget are decoded once (with decompression-bomb guards),
//     EXIF-orientation corrected, resized, and re-encoded down a
//     quality/edge ladder until they fit. GIFs always pass through
//     untouched to preserve animation.
//
// Every failure path degrades to "store the original bytes, skip vision
// inlining" — Process never returns a hard error and never panics, so a
// hostile upload cannot crash a request.
//
// All third-party codec imports (disintegration/imaging,
// golang.org/x/image/webp) are isolated in compress.go so a future swap
// to stdlib-only codecs touches exactly one file.
package imageproc
