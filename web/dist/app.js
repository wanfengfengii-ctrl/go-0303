// Live dashboard that talks to the real Go backend. No static demo data.
const api = async (path, opts) => {
  const res = await fetch(path, opts);
  const body = await res.json().catch(() => ({}));
  return { ok: res.ok, status: res.status, body };
};

const $ = (id) => document.getElementById(id);

async function renderHealth() {
  const { ok, status, body } = await api("/api/health");
  $("health").textContent = JSON.stringify({ ok, status, body }, null, 2);
}

async function renderCatalog() {
  const { body } = await api("/api/catalog");
  const list = (body && body.summaries) || [];
  $("catalog").textContent = list.length
    ? list.map((s) => `${s.section} · ${s.ring_type} · ${s.summary.slice(0, 12)}…`).join("\n")
    : "目录为空";
}

// Build a valid universal-wedge ring for the "澄江路—望塔站" section and lock it.
function buildLock(summary) {
  const groove = { width_mm: 12, depth_mm: 8, corner_mm: 4, joint_pos_mm: 20 };
  const holes = { count: 12, spacing_mm: 60 };
  const specs = [
    ["key", 30, "none"],
    ["adj", 60, "left"],
    ["adj", 60, "left"],
    ["std", 70, "left"],
    ["std", 70, "left"],
    ["std", 70, "left"],
  ];
  const segments = specs.map(([type, angle, wedge], i) => ({
    seq: i, type, center_angle: angle, wedge, groove, holes,
  }));
  const joints = [];
  for (let i = 0; i < 6; i++) {
    joints.push({
      type: "longitudinal",
      edge_a: { segment_seq: i, side: "right" },
      edge_b: { segment_seq: (i + 1) % 6, side: "left" },
    });
    joints.push({
      type: "circumferential",
      edge_a: { segment_seq: i, side: "front" },
      edge_b: { segment_seq: i, side: "back" },
    });
  }
  return {
    operation_id: `web-lock-${Date.now()}`,
    section: "澄江路—望塔站",
    ring_no: 1,
    ring_type: "通用楔形环",
    generation: 1,
    rule_summary: summary,
    logical_time: 0,
    segments,
    joints,
  };
}

async function lockTask() {
  const { body } = await api("/api/catalog");
  const first = (body.summaries || [])[0];
  if (!first) {
    $("graph").textContent = "目录为空，无法锁定任务。";
    return;
  }
  const res = await api("/api/rings", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(buildLock(first.summary)),
  });
  if (res.ok && res.body.task) {
    $("graph").textContent = JSON.stringify(
      { code: res.body.code, id: res.body.task.id, segments: res.body.task.segments.map((s) => ({ seq: s.seq, type: s.type, angle: s.center_angle })) },
      null, 2,
    );
  } else {
    $("graph").textContent = JSON.stringify(res.body, null, 2);
  }
}

document.getElementById("refresh").addEventListener("click", () => {
  renderHealth();
  renderCatalog();
});
document.getElementById("lock").addEventListener("click", lockTask);

renderHealth();
renderCatalog();
lockTask();
