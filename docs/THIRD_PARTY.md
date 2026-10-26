# Licensing and third-party notices

The root [MIT license](../LICENSE) covers Trill’s original source code, scripts, documentation, website content, and original artwork (including icons and logos). Original screenshots and demo recordings are included to the extent of Trill’s rights; third-party interfaces, fonts, names, and other third-party material shown within them retain their owners’ rights. This license does not grant trademark rights or imply endorsement by Apple, Spotify, or a session provider.

Vendored and separately licensed files retain their own license notices; the root license does not relicense them. Preserve these notices when redistributing.

Trill links these separately distributed Swift packages. The linked SwiftTerm and swift-mosh MIT license texts are bundled in the app’s Acknowledgements screen. Source checkouts retain their own license files.

- [SwiftTerm](https://github.com/migueldeicaza/SwiftTerm), MIT, Miguel de Icaza and contributors. Vendored revision `c3436be32e3dc26d5e2181b1e451d91e21e50d53` with the opt-in touch-link patch documented in `vendor/SwiftTerm/TRILL-PATCHES.md`.
- [swift-mosh](https://github.com/wiedymi/swift-mosh), MIT, its contributors. Vendored from revision `966bd6fc9c7e4a7cf0ff514e313ca1e456e45494`; [local transport patches](../vendor/swift-mosh/TRILL-PATCHES.md) cover process-relative timestamps, peer liveness, startup cancellation, and incoming-data limits.
- SwiftTerm's resolved package graph also includes [swift-argument-parser](https://github.com/apple/swift-argument-parser), Apache 2.0 with Swift Runtime Library Exception; [swift-png](https://github.com/tayloraswift/swift-png), Apache 2.0; and [h](https://github.com/rarestype/h), Apache 2.0. See `Package.resolved` for precise revisions. Not every graph dependency is linked on iOS.

The Go setup CLI links [go-qrcode](https://github.com/skip2/go-qrcode), MIT, Tom Harwood, pinned to `v0.0.0-20200617195104-da1b6568686e`. Its license is included in [licenses/go-qrcode.txt](licenses/go-qrcode.txt). QR generation runs locally.

On the computer, mosh-server and tmux are external executables installed by the user, not redistributed within Trill. Xirp is a separate Spotify application, used through its CLI. No Xirp implementation or artwork is copied into this repository.
