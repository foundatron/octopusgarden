package limits

// MaxResponseBytes is the maximum bytes captured from response bodies and command output (10 MB).
// Typed int64 to match io.LimitReader and related APIs directly.
const MaxResponseBytes int64 = 10 << 20
