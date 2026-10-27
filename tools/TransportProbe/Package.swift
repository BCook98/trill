// swift-tools-version: 6.0
import PackageDescription
let package = Package(name: "TransportProbe", platforms: [.macOS(.v13)], dependencies: [
    .package(path: "../../vendor/SwiftTerm")
], targets: [.executableTarget(name: "HerdrTransportProbe", dependencies: [.product(name: "SwiftTerm", package: "SwiftTerm")])])
