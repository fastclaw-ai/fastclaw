"use client";

import {
  useMemo,
  type ComponentProps,
  type MouseEvent as ReactMouseEvent,
  type WheelEvent as ReactWheelEvent,
} from "react";
import { useTranslations } from "next-intl";
import { Streamdown, defaultUrlTransform, type Components, type UrlTransform } from "streamdown";
import { createCodePlugin } from "@streamdown/code";
import { mermaid } from "@streamdown/mermaid";
import { math } from "@streamdown/math";
import { cjk } from "@streamdown/cjk";
import remarkBreaks from "remark-breaks";
import { fileUrl } from "@/lib/api";
import type { KnowledgeSource } from "@/lib/api";
import { ExternalAnchor } from "@/components/markdown-link";

// Streamdown 2.x splits rendering features into opt-in plugins. Without these,
// fenced code lands as an unstyled <pre> (no highlight), ```mermaid stays as
// text, $math$ doesn't resolve, and long CJK runs break awkwardly. Shiki theme
// stays on github-light / github-dark; the surrounding `dark:` context picks
// the side. Ported from fleet's MarkdownText.
const code = createCodePlugin({ themes: ["github-light", "github-dark"] });

// remark-breaks turns a single newline into <br>, which chat messages rely on
// (IM-style line breaks). It MUST run AFTER remarkGfm: run it before and it
// rewrites the newlines between table rows into <br>, so remarkGfm never sees a
// table and every table silently degrades to plain text. Streamdown runs the
// top-level `remarkPlugins` prop BEFORE gfm, so we inject it into the cjk
// plugin's `remarkPluginsAfter` slot, which runs post-gfm. (Verified end-to-end:
// via the prop → no <table>; via remarkPluginsAfter → <table> + <br> both render.)
const cjkWithBreaks = { ...cjk, remarkPluginsAfter: [...cjk.remarkPluginsAfter, remarkBreaks] };
const streamdownPlugins = { code, mermaid, math, cjk: cjkWithBreaks };

// Prose typography tuned for chat density (heading sizes, tight spacing),
// mirroring the former CHAT_PROSE_CLASS. The bulky overrides that flatten
// Streamdown's card chrome live in globals.css under the `.chat-md` class.
const PROSE_CLASS =
  "chat-md text-[13.5px] leading-normal prose prose-sm max-w-none dark:prose-invert min-w-0 wrap-anywhere " +
  "prose-p:my-1.5 " +
  // Tighter, shallower lists: smaller indent (pl-5 ≈ 20px vs prose's ~26px),
  // less gap between the marker and text, and snug item spacing.
  "prose-ul:my-1.5 prose-ol:my-1.5 prose-ul:pl-4 prose-ol:pl-4 " +
  "prose-li:my-0.5 prose-li:pl-0 prose-li:marker:text-muted-foreground/60 " +
  "prose-headings:font-semibold prose-headings:mt-2.5 prose-headings:mb-1 " +
  "prose-h1:text-[15px] prose-h2:text-[14px] prose-h3:text-[13.5px] prose-h4:text-[13.5px] prose-h5:text-[13.5px] prose-h6:text-[13.5px] " +
  "prose-blockquote:border-l-primary/60 prose-blockquote:bg-muted/20 prose-blockquote:px-3 prose-blockquote:not-italic " +
  "prose-a:text-primary prose-a:underline-offset-2 hover:prose-a:opacity-80 " +
  "prose-table:my-2 prose-table:text-[13px] prose-th:bg-muted/40 prose-th:font-medium prose-th:border-border prose-td:border-border " +
  "prose-th:py-1 prose-th:px-2 prose-td:py-1 prose-td:px-2 prose-td:leading-snug " +
  "prose-hr:my-3";

/**
 * ChatMarkdown is the single markdown rendering primitive for chat bubbles and
 * file previews. It wraps Streamdown (a streaming-aware superset of
 * react-markdown) so chat content gains Shiki code highlighting, KaTeX math,
 * Mermaid diagrams, and CJK-aware line breaking.
 *
 * Pass `agentId` (+ `sessionId`) for agent chat bubbles so the sandbox
 * `/workspace/<name>` paths the model emits resolve to the authenticated file
 * API; omit them for file previews / the standalone chat page where there's no
 * workspace to map.
 */
export function ChatMarkdown({
  text,
  agentId,
  sessionId,
  bareCode = false,
  knowledgeSources,
  onKnowledgeCitationClick,
}: {
  text: string;
  agentId?: string;
  sessionId?: string;
  // File-viewer mode: hide the floating copy pill on code blocks (the .chat-md
  // strip already removes the card) so a source file reads as plain code.
  bareCode?: boolean;
  knowledgeSources?: KnowledgeSource[];
  onKnowledgeCitationClick?: (source: KnowledgeSource) => void;
}) {
  const t = useTranslations("chat");
  const knowledgeByID = useMemo(() => {
    const map = new Map<string, KnowledgeSource>();
    for (const source of knowledgeSources || []) {
      if (source.id) map.set(source.id, source);
    }
    return map;
  }, [knowledgeSources]);
  const renderedText = useMemo(() => {
    if (knowledgeByID.size === 0) return text;
    return text.replace(/\[(K\d+)\]/g, (match, id: string) => {
      if (!knowledgeByID.has(id)) return match;
      return `[${id}](#knowledge-${id})`;
    });
  }, [knowledgeByID, text]);

  const components = useMemo<Components>(() => ({
    a: ({ node, ...props }: ComponentProps<"a"> & { node?: unknown }) => {
      void node;
      const href = typeof props.href === "string" ? props.href : "";
      if (href.startsWith("#knowledge-")) {
        const id = href.slice("#knowledge-".length);
        const source = knowledgeByID.get(id);
        return (
          <button
            type="button"
            className="rounded bg-primary/10 px-1 font-medium text-primary hover:bg-primary/15"
            title={source ? (source.chunk ? t("citationTooltip", { file: source.file, chunk: source.chunk }) : source.file) : id}
            onClick={(event) => {
              event.preventDefault();
              if (source) onKnowledgeCitationClick?.(source);
            }}
          >
            {props.children}
          </button>
        );
      }
      return <ExternalAnchor {...props} />;
    },
  }), [knowledgeByID, onKnowledgeCitationClick, t]);

  // Build the URL transform once per agent/session. A stable identity keeps
  // Streamdown (a memo component) from re-rendering on every streamed keystroke,
  // which a fresh inline function each render would defeat.
  const urlTransform = useMemo<UrlTransform>(() => {
    return (url, key, node) => {
      // Inline base64 images pass through (the default transform strips data:).
      if (key === "src" && url.startsWith("data:image/")) return url;
      // Remap sandbox `/workspace/<name>` (image src + link href) to the
      // authenticated file API. The docker bind-mount is session-scoped, so
      // prepend sessions/<sid>/ or the file API resolves against the agent root
      // and 404s.
      if (agentId && (key === "src" || key === "href") && url.startsWith("/workspace/")) {
        const rel = url.slice("/workspace/".length);
        return fileUrl(agentId, sessionId ? `sessions/${sessionId}/${rel}` : rel);
      }
      return defaultUrlTransform(url, key, node);
    };
  }, [agentId, sessionId]);

  // Click anywhere on a mermaid diagram → fullscreen. Streamdown renders a
  // hidden fullscreen toggle inside the block; we delegate the click to it.
  function onMermaidClick(e: ReactMouseEvent<HTMLDivElement>) {
    const target = e.target as HTMLElement;
    if (target.closest("button, a")) return;
    target
      .closest<HTMLElement>("[data-streamdown=mermaid-block]")
      ?.querySelector<HTMLButtonElement>('button[title*="ull" i], button[aria-label*="ull" i]')
      ?.click();
  }

  // mermaid.js attaches a {passive:false} wheel listener that preventDefaults
  // and would swallow chat scrolling. Catch the wheel in CAPTURE phase and
  // stopPropagation so mermaid's handler never sees it.
  function onWheelCapture(e: ReactWheelEvent<HTMLDivElement>) {
    if ((e.target as HTMLElement).closest("[data-streamdown=mermaid-block]")) {
      e.stopPropagation();
    }
  }

  return (
    <div className={bareCode ? PROSE_CLASS + " chat-md-bare" : PROSE_CLASS} onClick={onMermaidClick} onWheelCapture={onWheelCapture}>
      <Streamdown
        parseIncompleteMarkdown
        plugins={streamdownPlugins}
        urlTransform={urlTransform}
        components={components}
        controls={{
          table: true,
          code: true,
          // Minimal inline mermaid: no pan/zoom (intercepts wheel, blocks chat
          // scroll), no copy/download clutter. Keep fullscreen — clicking the
          // block triggers it (onMermaidClick); the modal re-enables pan/zoom.
          mermaid: { panZoom: false, copy: false, download: false, fullscreen: true },
        }}
      >
        {renderedText}
      </Streamdown>
    </div>
  );
}
