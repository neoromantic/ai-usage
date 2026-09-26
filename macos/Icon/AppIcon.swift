// Draws the app icon into an .iconset folder, which iconutil turns into
// AppIcon.icns: a gauge on a deep blue squircle, its arc running from green
// to red and its needle past the middle.
//
//   xcrun swiftc -O AppIcon.swift -o appicon && ./appicon AppIcon.iconset
import CoreGraphics
import Foundation
import ImageIO
import UniformTypeIdentifiers

/// The icon on Apple's 1024-point grid: the shape is 824 points square with
/// 100 around it, where its shadow falls.
let canvas: CGFloat = 1024
let body = CGRect(x: 100, y: 100, width: 824, height: 824)

func color(_ hex: UInt32, _ alpha: CGFloat = 1) -> CGColor {
    CGColor(srgbRed: CGFloat(hex >> 16 & 0xFF) / 255, green: CGFloat(hex >> 8 & 0xFF) / 255,
            blue: CGFloat(hex & 0xFF) / 255, alpha: alpha)
}

/// The macOS icon shape, a superellipse, which rounds its corners more
/// gradually than a rounded rectangle does.
func squircle(in r: CGRect) -> CGPath {
    let path = CGMutablePath()
    let n: CGFloat = 5
    let steps = 720
    for i in 0..<steps {
        let t = CGFloat(i) / CGFloat(steps) * 2 * .pi
        let c = cos(t), s = sin(t)
        let x = r.midX + r.width / 2 * copysign(pow(abs(c), 2 / n), c)
        let y = r.midY + r.height / 2 * copysign(pow(abs(s), 2 / n), s)
        i == 0 ? path.move(to: CGPoint(x: x, y: y)) : path.addLine(to: CGPoint(x: x, y: y))
    }
    path.closeSubpath()
    return path
}

/// A point on a circle, the angle in degrees counterclockwise from east.
func point(_ center: CGPoint, _ radius: CGFloat, _ degrees: CGFloat) -> CGPoint {
    let a = degrees * .pi / 180
    return CGPoint(x: center.x + radius * cos(a), y: center.y + radius * sin(a))
}

/// A color between green, yellow, orange and red, by how far along the arc.
func arcColor(_ f: CGFloat) -> CGColor {
    let stops: [(CGFloat, CGFloat, CGFloat)] = [
        (0.20, 0.78, 0.35), (1.00, 0.80, 0.00), (1.00, 0.58, 0.00), (1.00, 0.23, 0.19),
    ]
    let x = min(max(f, 0), 1) * CGFloat(stops.count - 1)
    let i = min(Int(x), stops.count - 2)
    let t = x - CGFloat(i)
    let a = stops[i], b = stops[i + 1]
    return CGColor(srgbRed: a.0 + (b.0 - a.0) * t, green: a.1 + (b.1 - a.1) * t, blue: a.2 + (b.2 - a.2) * t, alpha: 1)
}

func draw(_ ctx: CGContext) {
    let shape = squircle(in: body)

    // The body with a soft shadow under it.
    ctx.saveGState()
    ctx.setShadow(offset: CGSize(width: 0, height: -12), blur: 28, color: color(0x000000, 0.35))
    ctx.addPath(shape)
    ctx.setFillColor(color(0x1B2244))
    ctx.fillPath()
    ctx.restoreGState()

    ctx.saveGState()
    ctx.addPath(shape)
    ctx.clip()
    let background = CGGradient(colorsSpace: CGColorSpace(name: CGColorSpace.sRGB),
                                colors: [color(0x3A4FA8), color(0x1B2244)] as CFArray, locations: [0, 1])!
    ctx.drawLinearGradient(background, start: CGPoint(x: 512, y: body.maxY), end: CGPoint(x: 512, y: body.minY), options: [])
    let glow = CGGradient(colorsSpace: CGColorSpace(name: CGColorSpace.sRGB),
                          colors: [color(0x6F86FF, 0.35), color(0x6F86FF, 0)] as CFArray, locations: [0, 1])!
    ctx.drawRadialGradient(glow, startCenter: CGPoint(x: 512, y: 540), startRadius: 0,
                           endCenter: CGPoint(x: 512, y: 540), endRadius: 400, options: [])
    ctx.restoreGState()

    // The gauge: a 240-degree arc open at the bottom.
    let center = CGPoint(x: 512, y: 452)
    let radius: CGFloat = 272
    let start: CGFloat = 210, sweep: CGFloat = 240
    let filled: CGFloat = 0.68
    let width: CGFloat = 64

    ctx.setLineCap(.round)
    ctx.setLineWidth(width)
    ctx.setStrokeColor(color(0xFFFFFF, 0.14))
    ctx.addArc(center: center, radius: radius, startAngle: start * .pi / 180,
               endAngle: (start - sweep) * .pi / 180, clockwise: true)
    ctx.strokePath()

    // The filled part in short strokes, each in its own color, so the arc
    // shades from green to red.
    let pieces = 160
    for i in 0..<pieces {
        let f0 = CGFloat(i) / CGFloat(pieces) * filled
        let f1 = CGFloat(i + 1) / CGFloat(pieces) * filled
        ctx.setLineCap(i == 0 || i == pieces - 1 ? .round : .butt)
        ctx.setStrokeColor(arcColor(f0 / filled * 0.85))
        ctx.addArc(center: center, radius: radius, startAngle: (start - sweep * f0) * .pi / 180,
                   endAngle: (start - sweep * min(f1 + 0.002, filled)) * .pi / 180, clockwise: true)
        ctx.strokePath()
    }

    // Dots inside the arc at every sixth of it.
    ctx.setFillColor(color(0xFFFFFF, 0.55))
    for i in 0...6 {
        let p = point(center, radius - 88, start - sweep * CGFloat(i) / 6)
        ctx.fillEllipse(in: CGRect(x: p.x - 13, y: p.y - 13, width: 26, height: 26))
    }

    // The needle, tapering to its tip, over a round hub.
    let angle = start - sweep * filled
    let tip = point(center, radius - 40, angle)
    let left = point(center, 22, angle + 90)
    let right = point(center, 22, angle - 90)
    ctx.saveGState()
    ctx.setShadow(offset: CGSize(width: 0, height: -6), blur: 14, color: color(0x000000, 0.4))
    ctx.setFillColor(color(0xFFFFFF))
    ctx.move(to: left)
    ctx.addLine(to: tip)
    ctx.addLine(to: right)
    ctx.closePath()
    ctx.fillPath()
    ctx.fillEllipse(in: CGRect(x: center.x - 50, y: center.y - 50, width: 100, height: 100))
    ctx.restoreGState()
    ctx.setFillColor(color(0x26306A))
    ctx.fillEllipse(in: CGRect(x: center.x - 20, y: center.y - 20, width: 40, height: 40))
}

func png(pixels: Int, to url: URL) throws {
    guard let ctx = CGContext(data: nil, width: pixels, height: pixels, bitsPerComponent: 8, bytesPerRow: 0,
                              space: CGColorSpace(name: CGColorSpace.sRGB)!,
                              bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)
    else { throw CocoaError(.fileWriteUnknown) }
    ctx.interpolationQuality = .high
    ctx.scaleBy(x: CGFloat(pixels) / canvas, y: CGFloat(pixels) / canvas)
    draw(ctx)
    guard let image = ctx.makeImage(),
          let out = CGImageDestinationCreateWithURL(url as CFURL, UTType.png.identifier as CFString, 1, nil)
    else { throw CocoaError(.fileWriteUnknown) }
    CGImageDestinationAddImage(out, image, nil)
    guard CGImageDestinationFinalize(out) else { throw CocoaError(.fileWriteUnknown) }
}

guard CommandLine.arguments.count == 2 else {
    FileHandle.standardError.write(Data("usage: appicon OUT.iconset\n".utf8))
    exit(2)
}
let folder = URL(fileURLWithPath: CommandLine.arguments[1], isDirectory: true)
do {
    try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
    for size in [16, 32, 128, 256, 512] {
        try png(pixels: size, to: folder.appendingPathComponent("icon_\(size)x\(size).png"))
        try png(pixels: size * 2, to: folder.appendingPathComponent("icon_\(size)x\(size)@2x.png"))
    }
} catch {
    FileHandle.standardError.write(Data("appicon: \(error.localizedDescription)\n".utf8))
    exit(1)
}
