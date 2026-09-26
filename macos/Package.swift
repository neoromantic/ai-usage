// swift-tools-version:5.9
import PackageDescription

// The default build system links through clang with --sysroot, from which
// clang does not read the SDK's version, so the app would record macOS 14 as
// the SDK it was built with and newer macOS would draw it in the old look.
// xcrun names the SDK in SDKROOT; handing clang -isysroot records its version.
let sdkVersion: [LinkerSetting] = Context.environment["SDKROOT"].map {
    [.unsafeFlags(["-Xclang-linker", "-isysroot", "-Xclang-linker", $0])]
} ?? []

let package = Package(
    name: "AIUsage",
    platforms: [.macOS(.v14)],
    products: [
        .executable(name: "AIUsageBar", targets: ["AIUsageBar"]),
    ],
    targets: [
        .target(name: "AIUsageKit"),
        .executableTarget(name: "AIUsageBar", dependencies: ["AIUsageKit"], linkerSettings: sdkVersion),
        .executableTarget(name: "AIUsageChecks", dependencies: ["AIUsageKit"]),
    ],
    swiftLanguageVersions: [.v5]
)
