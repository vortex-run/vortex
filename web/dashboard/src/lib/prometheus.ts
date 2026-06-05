// Minimal Prometheus text-exposition parser — no external dependency.

export interface Sample {
  labels: Record<string, string>;
  value: number;
}

export interface MetricFamily {
  name: string;
  help: string;
  type: string;
  samples: Sample[];
}

// parsePrometheusText parses Prometheus exposition text into metric families.
// It understands `# HELP`, `# TYPE`, and sample lines with optional labels.
export function parsePrometheusText(text: string): MetricFamily[] {
  const families = new Map<string, MetricFamily>();

  const family = (name: string): MetricFamily => {
    let f = families.get(name);
    if (!f) {
      f = { name, help: "", type: "untyped", samples: [] };
      families.set(name, f);
    }
    return f;
  };

  for (const raw of text.split("\n")) {
    const line = raw.trim();
    if (line === "") continue;

    if (line.startsWith("# HELP ")) {
      const rest = line.slice(7);
      const sp = rest.indexOf(" ");
      const name = sp === -1 ? rest : rest.slice(0, sp);
      family(name).help = sp === -1 ? "" : rest.slice(sp + 1);
      continue;
    }
    if (line.startsWith("# TYPE ")) {
      const rest = line.slice(7);
      const sp = rest.indexOf(" ");
      const name = sp === -1 ? rest : rest.slice(0, sp);
      family(name).type = sp === -1 ? "untyped" : rest.slice(sp + 1);
      continue;
    }
    if (line.startsWith("#")) continue;

    const sample = parseSampleLine(line);
    if (sample) {
      family(sample.metric).samples.push({ labels: sample.labels, value: sample.value });
    }
  }

  return [...families.values()];
}

interface ParsedSample {
  metric: string;
  labels: Record<string, string>;
  value: number;
}

// parseSampleLine parses a single `metric{labels} value` line.
function parseSampleLine(line: string): ParsedSample | null {
  const brace = line.indexOf("{");
  let metric: string;
  let labels: Record<string, string> = {};
  let remainder: string;

  if (brace === -1) {
    const sp = line.indexOf(" ");
    if (sp === -1) return null;
    metric = line.slice(0, sp);
    remainder = line.slice(sp + 1).trim();
  } else {
    metric = line.slice(0, brace);
    const close = line.indexOf("}", brace);
    if (close === -1) return null;
    labels = parseLabels(line.slice(brace + 1, close));
    remainder = line.slice(close + 1).trim();
  }

  const value = parseFloat(remainder.split(/\s+/)[0]);
  if (Number.isNaN(value)) return null;
  return { metric, labels, value };
}

// parseLabels parses a comma-separated `key="value"` label list.
function parseLabels(s: string): Record<string, string> {
  const out: Record<string, string> = {};
  // Match key="value" pairs (values may contain escaped quotes minimally).
  const re = /(\w+)="([^"]*)"/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(s)) !== null) {
    out[m[1]] = m[2];
  }
  return out;
}
