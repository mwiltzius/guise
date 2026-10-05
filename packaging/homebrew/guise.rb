# Homebrew formula for guise. It builds from source, so Homebrew needs only
# Go at build time and nothing at runtime on macOS (guises are served over a
# local NFS mount using the system's own client).
#
# Publish it in a tap (github.com/mwiltzius/homebrew-guise, Formula/guise.rb)
# and update url/sha256 for each release:
#   brew install mwiltzius/guise/guise
class Guise < Formula
  desc "Present files in a different guise, with personal information swapped out"
  homepage "https://github.com/mwiltzius/guise"
  url "https://github.com/mwiltzius/guise/archive/refs/tags/v0.1.0.tar.gz"
  sha256 "44d9236305665c27cd76c064b3e519e214f75e09d6d065c10c6c80d2e5cb7667"
  license "GPL-3.0-only"
  head "https://github.com/mwiltzius/guise.git", branch: "main"

  depends_on "go" => :build

  def install
    ldflags = "-X main.version=#{version}" # std_go_args adds -s -w
    system "go", "build", *std_go_args(ldflags: ldflags), "./cmd/guise"
  end

  def caveats
    on_linux do
      <<~EOS
        guise mounts guises with FUSE, which needs your distribution's fuse3
        package (it lets guise mount without root), e.g.:
          sudo apt install fuse3
      EOS
    end
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/guise --version")
    ENV["XDG_CONFIG_HOME"] = testpath/"config"
    pipe_output("#{bin}/guise vault init --plain", "")
    pipe_output("#{bin}/guise vault set lastname", "Wiltzius\n")
    assert_equal "lastname\n", shell_output("#{bin}/guise vault list")
  end
end
