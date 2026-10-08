import hljs from "highlight.js/lib/core";
import bash from "highlight.js/lib/languages/bash";
import c from "highlight.js/lib/languages/c";
import cpp from "highlight.js/lib/languages/cpp";
import css from "highlight.js/lib/languages/css";
import dockerfile from "highlight.js/lib/languages/dockerfile";
import go from "highlight.js/lib/languages/go";
import ini from "highlight.js/lib/languages/ini";
import java from "highlight.js/lib/languages/java";
import javascript from "highlight.js/lib/languages/javascript";
import json from "highlight.js/lib/languages/json";
import kotlin from "highlight.js/lib/languages/kotlin";
import makefile from "highlight.js/lib/languages/makefile";
import markdown from "highlight.js/lib/languages/markdown";
import php from "highlight.js/lib/languages/php";
import python from "highlight.js/lib/languages/python";
import ruby from "highlight.js/lib/languages/ruby";
import rust from "highlight.js/lib/languages/rust";
import scss from "highlight.js/lib/languages/scss";
import sql from "highlight.js/lib/languages/sql";
import swift from "highlight.js/lib/languages/swift";
import typescript from "highlight.js/lib/languages/typescript";
import xml from "highlight.js/lib/languages/xml";
import yaml from "highlight.js/lib/languages/yaml";

const langs = {
  bash, c, cpp, css, dockerfile, go, ini, java, javascript, json, kotlin, makefile,
  markdown, php, python, ruby, rust, scss, sql, swift, typescript, xml, yaml,
};
for (const [name, lang] of Object.entries(langs)) hljs.registerLanguage(name, lang);

const escapeMap: Record<string, string> = { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" };

export function escapeHTML(s: string): string {
  return s.replace(/[&<>"']/g, (ch) => escapeMap[ch]);
}

// highlight returns HTML for one line. Lines are highlighted on their own,
// so a construct spanning lines (a block comment) may color imperfectly;
// that keeps line numbers and diff rows aligned. mark wraps literal
// matches in <mark> when the line has no syntax coloring.
export function highlight(line: string, lang?: string, mark?: string): string {
  if (mark) return markLiteral(line, mark);
  if (!lang || !hljs.getLanguage(lang)) return escapeHTML(line);
  try {
    return hljs.highlight(line, { language: lang, ignoreIllegals: true }).value;
  } catch {
    return escapeHTML(line);
  }
}

function markLiteral(line: string, mark: string): string {
  const out: string[] = [];
  let i = 0;
  for (;;) {
    const j = line.indexOf(mark, i);
    if (j < 0 || mark === "") break;
    out.push(escapeHTML(line.slice(i, j)), "<mark>", escapeHTML(mark), "</mark>");
    i = j + mark.length;
  }
  out.push(escapeHTML(line.slice(i)));
  return out.join("");
}
