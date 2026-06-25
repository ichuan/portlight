(async () => {
  const version = document.querySelector("#latest-version");
  const list = document.querySelector("#download-list");
  if (!version || !list) return;

  try {
    const response = await fetch("releases/latest.json", { cache: "no-store" });
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    const release = await response.json();
    version.textContent = `Latest: ${release.version}`;
    list.textContent = "";
    for (const file of release.files || []) {
      const link = document.createElement("a");
      link.className = "download-link";
      link.href = file.url;
      link.textContent = `${file.os}/${file.arch}`;

      const meta = document.createElement("span");
      meta.textContent = `${formatBytes(file.size)} · ${String(file.sha256 || "").slice(0, 12)}...`;

      const row = document.createElement("div");
      row.append(link, meta);
      list.append(row);
    }
  } catch {
    version.textContent = "Latest release metadata has not been published yet.";
  }
})();

function formatBytes(value) {
  if (!Number.isFinite(value) || value <= 0) return "size unavailable";
  const units = ["B", "KB", "MB", "GB"];
  let size = value;
  let index = 0;
  while (size >= 1024 && index < units.length - 1) {
    size /= 1024;
    index += 1;
  }
  return `${size.toFixed(index === 0 ? 0 : 1)} ${units[index]}`;
}
