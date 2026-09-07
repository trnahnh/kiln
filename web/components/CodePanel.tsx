import type { ReactNode } from "react";

interface Props {
  code: string;
  lang: "yaml" | "json" | "text";
  title?: string;
}

// A small tokenizer for the YAML and JSON artifacts on the page; enough to separate
// keys, values, comments and punctuation without pulling in a highlighter.
function tokenize(line: string, lang: Props["lang"]): ReactNode[] {
  const out: ReactNode[] = [];
  let rest = line;
  let key = 0;
  const push = (cls: string, text: string) => {
    if (text) out.push(<span key={key++} className={cls}>{text}</span>);
  };
  if (lang === "text") {
    push("code-p", line);
    return out;
  }
  const commentAt = lang === "yaml" ? rest.indexOf(" #") : -1;
  let comment = "";
  if (lang === "yaml" && rest.trimStart().startsWith("#")) {
    push("code-c", rest);
    return out;
  }
  if (commentAt >= 0) {
    comment = rest.slice(commentAt);
    rest = rest.slice(0, commentAt);
  }
  const m = rest.match(/^(\s*-?\s*)("?[\w.\-/]+"?)(\s*:\s*)(.*)$/);
  if (m) {
    push("code-p", m[1]);
    push("code-k", m[2]);
    push("code-p", m[3]);
    push(/^["'\[{]/.test(m[4]) || /^[\d.]+$/.test(m[4]) ? "code-v" : "code-n", m[4]);
  } else {
    push(/^\s*[\[\]{}(),]*\s*$/.test(rest) ? "code-p" : "code-n", rest);
  }
  push("code-c", comment);
  return out;
}

export default function CodePanel({ code, lang, title }: Props) {
  return (
    <div className="panel overflow-hidden">
      {title && <p className="border-b border-hairline px-4 py-2.5 text-[13px] text-fg-muted">{title}</p>}
      <pre className="code overflow-x-auto px-4 py-4">
        {code.split("\n").map((line, i) => (
          <span key={i} className="block min-h-[1.6em]">
            {tokenize(line, lang)}
          </span>
        ))}
      </pre>
    </div>
  );
}
