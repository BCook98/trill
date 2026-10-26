// swift-tools-version: 6.0
import PackageDescription
let package = Package(name: "TransportProbe", platforms: [.macOS(.v13)], dependencies: [
    .package(path: "../../vendor/swift-mosh")
], targets: [.executableTarget(name: "TransportProbe", dependencies: [.product(name: "MoshCore", package: "swift-mosh")]), .executableTarget(name: "HerdrTransportProbe", dependencies: [.product(name: "MoshCore", package: "swift-mosh")])])
