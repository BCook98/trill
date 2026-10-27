# Licensing and third-party notices

The root [MIT license](../LICENSE) covers Trill’s original source code, scripts, documentation, website content, and original artwork (including icons and logos). Original screenshots and demo recordings are included to the extent of Trill’s rights; third-party interfaces, fonts, names, and other third-party material shown within them retain their owners’ rights. This license does not grant trademark rights or imply endorsement by Apple, Spotify, or a session provider.

Vendored and separately licensed files retain their own license notices; the root license does not relicense them. Preserve these notices when redistributing.

Trill links these separately distributed Swift packages. The SwiftTerm MIT license text is bundled in the app’s Acknowledgements screen. Source checkouts retain their own license files.

- [SwiftTerm](https://github.com/migueldeicaza/SwiftTerm), MIT, Miguel de Icaza and contributors. Vendored revision `c3436be32e3dc26d5e2181b1e451d91e21e50d53` with the opt-in touch-link patch documented in `vendor/SwiftTerm/TRILL-PATCHES.md`.
- SwiftTerm's resolved package graph also includes [swift-argument-parser](https://github.com/apple/swift-argument-parser), Apache 2.0 with Swift Runtime Library Exception; [swift-png](https://github.com/tayloraswift/swift-png), Apache 2.0; and [h](https://github.com/rarestype/h), Apache 2.0. See `Package.resolved` for precise revisions. Not every graph dependency is linked on iOS.

The Go setup CLI links [go-qrcode](https://github.com/skip2/go-qrcode), MIT, Tom Harwood, pinned to `v0.0.0-20200617195104-da1b6568686e`. Its license is included in [licenses/go-qrcode.txt](licenses/go-qrcode.txt). QR generation runs locally.

Herdr is installed separately by the user. Trill integrates with its native socket and supported CLI; it does not redistribute Herdr’s implementation or artwork.

The Go companion links the modules pinned in `sidecar/go.mod`, including coder/websocket for authenticated terminal streaming and Tailscale for embedded networking. Dependency source distributions retain their own licenses.
