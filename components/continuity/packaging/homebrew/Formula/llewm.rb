class Llewm < Formula
  desc "Local-first continuity for coding agents"
  homepage "https://github.com/llewm/llewm"
  url "https://github.com/llewm/llewm.git",
      revision: "43adc25cb586ac1aa4accd00dd34fa64e3dfb5bd"
  version "0.1.0-preview.20261013"
  license "MIT"

  depends_on "go" => :build
  depends_on "git"

  def fetch
    ENV["GOWORK"] = "off"
    ENV["GOTOOLCHAIN"] = "local"
    cd "components/continuity" do
      system "go", "mod", "download"
    end
  end

  def install
    ENV["GOWORK"] = "off"
    ENV["GOTOOLCHAIN"] = "local"
    ENV["GOPROXY"] = "off"
    ldflags = "-X github.com/llewm/llewm/components/continuity/internal/buildinfo.Version=#{version}"
    cd "components/continuity" do
      system "go", "build", *std_go_args(ldflags:), "-buildvcs=true", "./cmd/llewm"
    end
    pkgshare.install "LICENSE", "THIRD_PARTY_NOTICES.txt"
    generate_completions_from_executable(bin/"llewm", "completion")
  end

  def caveats
    <<~EOS
      This is a pinned source preview. Install the matching Llewm client plugin
      from commit 43adc25cb586ac1aa4accd00dd34fa64e3dfb5bd separately.
      Stop clients and MCP processes before upgrades; restart and verify afterward.
      Database migrations require an explicit integrity-checked backup and migrate
      operation. Installing or removing this formula does not migrate or delete
      project stores, global guidance, or private memory history.
    EOS
  end

  test do
    ENV["CONTINUITY_GLOBAL_HOME"] = (testpath/"global").to_s
    ENV["CONTINUITY_HOME"] = (testpath/"state").to_s
    system "git", "init", "--quiet", testpath
    (testpath/"AGENTS.md").write "# Formula qualification\nKeep project history.\n"
    info = JSON.parse(shell_output("#{bin}/llewm version --output json"))
    assert_equal version.to_s, info.fetch("application_version")
    assert_equal "43adc25cb586ac1aa4accd00dd34fa64e3dfb5bd", info.fetch("git_commit")
    assert_equal false, info.fetch("build_dirty")
    system bin/"llewm", "--project-root", testpath, "init"
    system bin/"llewm", "--project-root", testpath, "doctor"
    status = JSON.parse(shell_output("#{bin}/llewm --project-root #{testpath} database status"))
    assert_equal "current", status.fetch("state")
  end
end
