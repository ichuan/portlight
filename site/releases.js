const platformLabels = {
  darwin: "macOS",
  windows: "Windows",
  linux: "Linux",
};

const archLabels = {
  amd64: "Intel / AMD64",
  arm64: "ARM64",
};

(async () => {
  wireCopyButtons();
  wireTabs("linux");

  const version = document.querySelector("#latest-version");
  const primaryDownload = document.querySelector("#primary-download");

  try {
    const [release, detected] = await Promise.all([fetchRelease(), detectPlatform()]);
    renderRelease(release, detected);
    setActiveTab(detected.os || "linux");

    if (version) {
      version.textContent = `Latest ${release.version}`;
    }

    if (primaryDownload) {
      primaryDownload.href = "#downloads";
      primaryDownload.querySelector("span:last-child").textContent = "Install portlight";
    }
  } catch {
    if (version) {
      version.textContent = "Release info is unavailable.";
    }
    for (const node of document.querySelectorAll("[data-downloads]")) {
      node.textContent = "Release info is unavailable.";
    }
  }
})();

async function fetchRelease() {
  const response = await fetch("releases/latest.json", { cache: "no-store" });
  if (!response.ok) {
    throw new Error(`HTTP ${response.status}`);
  }
  return response.json();
}

async function detectPlatform() {
  const text = `${navigator.platform || ""} ${navigator.userAgent || ""}`.toLowerCase();
  let os = "";
  if (text.includes("mac")) {
    os = "darwin";
  } else if (text.includes("win")) {
    os = "windows";
  } else if (!text.includes("android") && (text.includes("linux") || text.includes("x11"))) {
    os = "linux";
  }

  let arch = "";
  if (navigator.userAgentData?.getHighEntropyValues) {
    try {
      const values = await navigator.userAgentData.getHighEntropyValues(["architecture"]);
      arch = normalizeArch(values.architecture);
    } catch {
      arch = "";
    }
  }
  if (!arch && /arm|aarch64/.test(text)) {
    arch = "arm64";
  }
  if (!arch && /x86_64|win64|wow64|amd64|intel/.test(text)) {
    arch = "amd64";
  }

  return { os, arch };
}

function normalizeArch(value) {
  const arch = String(value || "").toLowerCase();
  if (arch === "arm" || arch === "arm64" || arch === "aarch64") return "arm64";
  if (arch === "x86" || arch === "x86_64" || arch === "amd64") return "amd64";
  return "";
}

function renderRelease(release, detected) {
  const files = Array.isArray(release.files) ? release.files : [];

  for (const os of Object.keys(platformLabels)) {
    const card = document.querySelector(`[data-platform="${os}"]`);
    const list = document.querySelector(`[data-downloads="${os}"]`);
    const install = document.querySelector(`[data-install="${os}"]`);
    const candidates = files.filter((file) => file.os === os);
    const preferred = chooseFile(candidates, os, detected.arch);

    if (list) {
      list.replaceChildren(...candidates.map(renderFileLink));
      if (candidates.length === 0) {
        list.textContent = "No download is available for this platform.";
      }
    }
    if (install) {
      install.textContent = preferred ? installCommand(os) : "No download is available for this platform.";
    }
  }
}

function wireTabs(defaultOS) {
  for (const tab of document.querySelectorAll("[data-tab]")) {
    tab.addEventListener("click", () => setActiveTab(tab.dataset.tab || defaultOS));
  }
  setActiveTab(defaultOS);
}

function setActiveTab(os) {
  const selected = platformLabels[os] ? os : "linux";
  for (const tab of document.querySelectorAll("[data-tab]")) {
    const active = tab.dataset.tab === selected;
    tab.setAttribute("aria-selected", active ? "true" : "false");
    tab.tabIndex = active ? 0 : -1;
  }
  for (const panel of document.querySelectorAll("[data-platform]")) {
    panel.hidden = panel.dataset.platform !== selected;
  }
}

function renderFileLink(file) {
  const link = document.createElement("a");
  link.className = "download-file";
  link.href = file.url;
  link.setAttribute("download", file.name || "");

  const label = document.createElement("span");
  label.textContent = archLabels[file.arch] || file.arch || "Binary";

  const meta = document.createElement("small");
  meta.textContent = formatBytes(file.size);

  link.append(label, meta);
  return link;
}

function chooseFile(files, os, arch) {
  if (!Array.isArray(files) || files.length === 0) return null;
  const scoped = os ? files.filter((file) => file.os === os) : files;
  const candidates = scoped.length ? scoped : files;
  return (
    candidates.find((file) => file.arch === arch) ||
    candidates.find((file) => file.arch === "arm64") ||
    candidates.find((file) => file.arch === "amd64") ||
    candidates[0]
  );
}

function installCommand(os) {
  if (os === "windows") {
    return "irm https://portlight.616.pub/install.ps1 | iex";
  }
  return "curl -fsSL https://portlight.616.pub/install.sh | sh";
}

function wireCopyButtons() {
  for (const button of document.querySelectorAll("[data-copy-target]")) {
    button.addEventListener("click", async () => {
      const target = document.getElementById(button.dataset.copyTarget || "");
      const text = target?.textContent || "";
      if (!text || text.includes("loading")) return;

      try {
        await navigator.clipboard.writeText(text);
        button.textContent = "Copied";
        window.setTimeout(() => {
          button.textContent = "Copy";
        }, 1400);
      } catch {
        button.textContent = "Select";
      }
    });
  }
}

function formatBytes(value) {
  if (!Number.isFinite(value) || value <= 0) return "size unknown";
  const units = ["B", "KB", "MB", "GB"];
  let size = value;
  let index = 0;
  while (size >= 1024 && index < units.length - 1) {
    size /= 1024;
    index += 1;
  }
  return `${size.toFixed(index === 0 ? 0 : 1)} ${units[index]}`;
}
