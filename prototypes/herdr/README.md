# Herdr stream integration probe

The app and companion support only Herdr’s structured terminal stream over HTTPS. This probe creates its own temporary Herdr configuration, server, workspace, shell and loopback companion. It never attaches to personal terminals or starts a coding agent.

```sh
mkdir -p .local/herdr-prototype
go -C sidecar build -o ../.local/herdr-prototype/trilld ./cmd/trilld
swift build --package-path tools/TransportProbe --product HerdrTransportProbe
python3 prototypes/herdr/through-sidecar.py --herdr /absolute/path/herdr
```

The Swift client renders frames using SwiftTerm and verifies input, exact resize, explicit history scroll, OSC 8 targets and shell state across three attachments. The Python harness checks real socket event invalidation and terminal survival after release, then creates a native tab in an owned folder containing spaces and shell syntax, confirms its cwd through the file API, and closes exactly that tab. The probe refuses non-lab sessions. Only its own server and children are stopped, and its private diagnostic directory is reported on completion.

The loopback probe permits explicit HTTP development transport. Production and phone builds use authenticated HTTPS/WSS. This is not evidence of physical-device roaming, battery behavior or loss recovery.

## Historical transport comparison, 27 October 2026

The old transport and comparison executable have been removed. These recorded measurements explain the stream-only decision; they are not instructions to run a supported alternative.

Recorded on this Mac, Herdr 0.9.5, loopback, three attachments and 30 input round trips per transport:

| Check | Mosh | Herdr stream |
| --- | --- | --- |
| First attachment to first frame | 626 ms | 329 ms |
| Median input to rendered response | 109 ms | 19 ms |
| 95th percentile input to rendered response | 111 ms | 23 ms |
| Hidden OSC 8 hyperlink target preserved | No | Yes |
| Resize reaches shell | Passed | Passed |
| Fresh attachment retains shell variables | Passed | Passed |
| Explicit remote history scroll | Outside this headless probe | Passed |

Mosh emits incremental screen updates, so the benchmark checks the rendered screen rather than concatenating ANSI fragments. The hidden hyperlink check looks for an actual OSC 8 sequence, not a URL merely echoed in shell input. Input markers are unique across transports and attachments and do not appear literally in submitted commands. Mosh gesture scrolling needs SwiftTerm's actual mouse encoder and is not inferred from arbitrary mouse byte sequences.

A second run while native tests were running measured median input round trips of 106 ms for Mosh and 21 ms for the stream, with first-frame times of 589 ms and 593 ms respectively. Startup timing is sensitive to concurrent host work; the input-latency and hyperlink results were consistent. That run also confirmed a real workspace rename event reached the companion catalog. Herdr 0.9.5 did not emit an event for the tested pane-label rename, so polling remains necessary.

These are local functional/latency measurements, not mobile-network measurements. The original macOS comparison used the then-current transport libraries and SwiftTerm but does not run the phone UI. Physical-device Wi-Fi/cellular switching, loss recovery and battery behavior remain unmeasured. The native unit suite separately verifies that implicit and labeled links take precedence over remote mouse reporting through both tap entry points.
