# Template only: this formula is not installable as-is. Once a real
# v0.1.0 release exists (see `goreleaser release` in .goreleaser.yaml),
# copy this file into the `brandon/homebrew-saddle` tap repo at
# `Formula/saddle.rb` and replace the `sha256` value below with the
# checksum for `saddle_Darwin_arm64.tar.gz` from that release's
# `checksums.txt`.

class Saddle < Formula
  desc "Run Claude Code in a contained environment on macOS"
  homepage "https://github.com/brandon/saddle"
  url "https://github.com/brandon/saddle/releases/download/v0.1.0/saddle_Darwin_arm64.tar.gz"
  sha256 "REPLACE_WITH_VALUE_FROM_checksums.txt"
  license "Apache-2.0"

  depends_on "container"
  depends_on macos: :tahoe
  depends_on arch: :arm64

  def install
    bin.install "saddle"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/saddle version")
  end
end
