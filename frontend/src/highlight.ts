// Syntax highlighting for the preview pane's text view: highlight.js
// core plus an explicit set of registered languages -- never the full
// "highlight.js" bundle -- so Vite tree-shakes everything else out.
// The registered names cover every hint internal/preview's LangHint
// emits (plus "shell" and "plaintext"); "zig" is hinted Go-side but
// has no grammar in the highlight.js distribution, so it falls back
// to auto-detection like any unregistered name.
//
// THE ONE innerHTML-STYLE SINK in this frontend lives here (see
// setHighlighted); every other render path is text-node-only.

import hljs from "highlight.js/lib/core";
import bash from "highlight.js/lib/languages/bash";
import c from "highlight.js/lib/languages/c";
import cpp from "highlight.js/lib/languages/cpp";
import csharp from "highlight.js/lib/languages/csharp";
import css from "highlight.js/lib/languages/css";
import diff from "highlight.js/lib/languages/diff";
import dockerfile from "highlight.js/lib/languages/dockerfile";
import go from "highlight.js/lib/languages/go";
import ini from "highlight.js/lib/languages/ini";
import java from "highlight.js/lib/languages/java";
import javascript from "highlight.js/lib/languages/javascript";
import json from "highlight.js/lib/languages/json";
import kotlin from "highlight.js/lib/languages/kotlin";
import lua from "highlight.js/lib/languages/lua";
import makefile from "highlight.js/lib/languages/makefile";
import markdown from "highlight.js/lib/languages/markdown";
import php from "highlight.js/lib/languages/php";
import plaintext from "highlight.js/lib/languages/plaintext";
import python from "highlight.js/lib/languages/python";
import ruby from "highlight.js/lib/languages/ruby";
import rust from "highlight.js/lib/languages/rust";
import scss from "highlight.js/lib/languages/scss";
import shell from "highlight.js/lib/languages/shell";
import sql from "highlight.js/lib/languages/sql";
import swift from "highlight.js/lib/languages/swift";
import typescript from "highlight.js/lib/languages/typescript";
import vim from "highlight.js/lib/languages/vim";
import xml from "highlight.js/lib/languages/xml";
import yaml from "highlight.js/lib/languages/yaml";
import "./hljs-theme.css";

// Auto-detection cap: highlightAuto scores the content against every
// registered grammar, so it only runs on reasonably small content.
const AUTO_DETECT_MAX_CHARS = 64 * 1024;

hljs.registerLanguage("bash", bash);
hljs.registerLanguage("c", c);
hljs.registerLanguage("cpp", cpp);
hljs.registerLanguage("csharp", csharp);
hljs.registerLanguage("css", css);
hljs.registerLanguage("diff", diff);
hljs.registerLanguage("dockerfile", dockerfile);
hljs.registerLanguage("go", go);
hljs.registerLanguage("ini", ini);
hljs.registerLanguage("java", java);
hljs.registerLanguage("javascript", javascript);
hljs.registerLanguage("json", json);
hljs.registerLanguage("kotlin", kotlin);
hljs.registerLanguage("lua", lua);
hljs.registerLanguage("makefile", makefile);
hljs.registerLanguage("markdown", markdown);
hljs.registerLanguage("php", php);
hljs.registerLanguage("plaintext", plaintext);
hljs.registerLanguage("python", python);
hljs.registerLanguage("ruby", ruby);
hljs.registerLanguage("rust", rust);
hljs.registerLanguage("scss", scss);
hljs.registerLanguage("shell", shell);
hljs.registerLanguage("sql", sql);
hljs.registerLanguage("swift", swift);
hljs.registerLanguage("typescript", typescript);
hljs.registerLanguage("vim", vim);
hljs.registerLanguage("xml", xml);
hljs.registerLanguage("yaml", yaml);

// setHighlighted parses highlight.js markup into the <code> node.
//
// INVARIANT: only hljs.highlight / hljs.highlightAuto output may pass
// through here. The repo-wide no-innerHTML rule exists to keep
// untrusted file/plugin data out of the DOM as markup; hljs output
// interleaves <span> token tags with the content text, so text-node
// building cannot carry it -- and parsing it is safe ONLY because
// highlight.js HTML-escapes all content text itself (its documented
// contract): file bytes never reach the emitted value unescaped.
// Never route any other string through this function, and never widen
// its reach beyond the single <code> node it is handed.
function setHighlighted(code: HTMLElement, hljsMarkup: string): void {
  const frag = document.createRange().createContextualFragment(hljsMarkup);
  code.replaceChildren(frag);
}

// highlightInto renders content into the (empty) code element: with
// the payload's language when it is registered, auto-detected over
// the registered set for small unhinted content, and as plain text
// for big unhinted content or on any highlighting error.
export function highlightInto(
  code: HTMLElement,
  content: string,
  lang: string,
): void {
  try {
    if (lang !== "" && hljs.getLanguage(lang) !== undefined) {
      setHighlighted(
        code,
        hljs.highlight(content, { language: lang, ignoreIllegals: true })
          .value,
      );
      return;
    }
    if (content.length <= AUTO_DETECT_MAX_CHARS) {
      setHighlighted(code, hljs.highlightAuto(content).value);
      return;
    }
  } catch (err) {
    console.warn("preview highlight failed: " + String(err));
  }
  code.textContent = content; // plain-text fallback
}
