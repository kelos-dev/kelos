class Kelos < Formula
  desc "Orchestrate autonomous AI coding agents on Kubernetes"
  homepage "https://github.com/kelos-dev/kelos"
  license "Apache-2.0"

  version "VERSION_PLACEHOLDER"

  on_macos do
    on_intel do
      url "https://github.com/kelos-dev/kelos/releases/download/v#{version}/kelos-darwin-amd64"
      sha256 "SHA256_MACOS_AMD64_PLACEHOLDER"
    end
    on_arm do
      url "https://github.com/kelos-dev/kelos/releases/download/v#{version}/kelos-darwin-arm64"
      sha256 "SHA256_MACOS_ARM64_PLACEHOLDER"
    end
  end

  on_linux do
    on_intel do
      url "https://github.com/kelos-dev/kelos/releases/download/v#{version}/kelos-linux-amd64"
      sha256 "SHA256_LINUX_AMD64_PLACEHOLDER"
    end
    on_arm do
      url "https://github.com/kelos-dev/kelos/releases/download/v#{version}/kelos-linux-arm64"
      sha256 "SHA256_LINUX_ARM64_PLACEHOLDER"
    end
  end

  def install
    # Homebrew downloads the binary with the URL's filename,
    # so we just rename it and install
    bin.install Dir.glob("kelos-*").first => "kelos"
  end

  test do
    output = shell_output("#{bin}/kelos version")
    assert_match(/\d+\.\d+\.\d+/, output)
  end
end
