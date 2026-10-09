"use client";

import {
  useMemo,
  type ComponentProps,
  type MouseEvent as ReactMouseEvent,
  type WheelEvent as ReactWheelEvent,
} from "react";
import { Streamdown, defaultRehypePlugins, defaultUrlTransform, type Components, type UrlTransform } from "streamdown";
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
  // Tighter lists: snug item spacing. The indent is set in globals.css
  // (.chat-md lists) so top-level and nested lists share it.
  "prose-ul:my-1.5 prose-ol:my-1.5 " +
  "prose-li:my-0.5 prose-li:pl-0 prose-li:marker:text-muted-foreground/60 " +
  "prose-headings:font-semibold prose-headings:mt-2.5 prose-headings:mb-1 " +
  "prose-h1:text-[15px] prose-h2:text-[14px] prose-h3:text-[13.5px] prose-h4:text-[13.5px] prose-h5:text-[13.5px] prose-h6:text-[13.5px] " +
  "prose-blockquote:border-l-primary/60 prose-blockquote:bg-muted/20 prose-blockquote:px-3 prose-blockquote:not-italic " +
  "prose-a:text-primary prose-a:underline-offset-2 hover:prose-a:opacity-80 " +
  "prose-table:my-2 prose-table:text-[13px] prose-th:bg-muted/40 prose-th:font-medium prose-th:border-border prose-td:border-border " +
  "prose-th:py-1 prose-th:px-2 prose-td:py-1 prose-td:px-2 prose-td:leading-snug " +
  "prose-hr:my-3";

// agentWorkspacePath classifies a markdown URL that points into the agent's
// workspace: an absolute host path under workspaces/<agentId>/ yields its
// path from the agent root; a relative path yields itself with
// relative=true (the caller scopes it to the session). null for anything
// else (another scheme, an in-page anchor, an app route, a path outside
// this agent's workspace).
function agentWorkspacePath(url: string, agentId: string): { path: string; relative: boolean } | null {
  if (!url || /^[a-z][a-z0-9+.-]*:/i.test(url) || /^[#?]/.test(url) || url.startsWith("//")) return null;
  if (url.startsWith("/")) {
    const marker = `/workspaces/${agentId}/`;
    const at = url.indexOf(marker);
    return at >= 0 ? { path: url.slice(at + marker.length), relative: false } : null;
  }
  const rel = url.replace(/^(\.\/)+/, "");
  // Stay inside the workspace; the server rejects escapes anyway.
  return rel && !rel.split("/").includes("..") ? { path: rel, relative: true } : null;
}

// resolveWorkspaceUrl maps a URL the model wrote for a workspace file to its
// path relative to the agent root (what fileUrl takes), or null when it isn't
// one. Sandbox `/workspace/<name>` and host-mode relative paths are both
// session-scoped (the docker bind-mount and the host exec cwd are the session
// workspace); an absolute host path under workspaces/<agentId>/ is exact.
function resolveWorkspaceUrl(url: string, agentId: string, sessionId?: string): string | null {
  const scoped = (rel: string) => (sessionId ? `sessions/${sessionId}/${rel}` : rel);
  if (url.startsWith("/workspace/")) return scoped(url.slice("/workspace/".length));
  const target = agentWorkspacePath(url, agentId);
  if (!target) return null;
  return target.relative ? scoped(target.path) : target.path;
}

type RehypePlugins = NonNullable<ComponentProps<typeof Streamdown>["rehypePlugins"]>;

type HastNode = { type?: string; tagName?: string; properties?: Record<string, unknown>; children?: HastNode[] };

// rehypeWorkspaceUrls rewrites workspace file references on <img src> and
// <a href> to the authenticated file API. It must run BEFORE rehype-harden:
// harden blocks any URL it can't parse, and a bare relative path like
// `report.html` isn't parseable — urlTransform runs too late to save it, so
// such links rendered as "report.html [blocked]".
function rehypeWorkspaceUrls(options: { agentId: string; sessionId?: string }) {
  return (tree: HastNode) => {
    const walk = (node: HastNode) => {
      if (node.type === "element" && node.properties) {
        const attr = node.tagName === "img" ? "src" : node.tagName === "a" ? "href" : "";
        const value = attr ? node.properties[attr] : undefined;
        if (typeof value === "string") {
          const rel = resolveWorkspaceUrl(value, options.agentId, options.sessionId);
          if (rel) {
            node.properties[attr] = fileUrl(options.agentId, rel);
            if (attr === "href") node.properties.dataWorkspacePath = rel;
          }
        }
      }
      node.children?.forEach(walk);
    };
    walk(tree);
  };
}

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
  onWorkspaceFileClick,
}: {
  text: string;
  agentId?: string;
  sessionId?: string;
  // File-viewer mode: hide the floating copy pill on code blocks (the .chat-md
  // strip already removes the card) so a source file reads as plain code.
  bareCode?: boolean;
  knowledgeSources?: KnowledgeSource[];
  onKnowledgeCitationClick?: (source: KnowledgeSource) => void;
  // Opens a workspace file (agent-root-relative path) in the in-app viewer.
  // Without it, workspace links open in a new tab.
  onWorkspaceFileClick?: (path: string) => void;
}) {
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
            title={source ? (source.chunk ? `${source.file}, chunk ${source.chunk}` : source.file) : id}
            onClick={(event) => {
              event.preventDefault();
              if (source) onKnowledgeCitationClick?.(source);
            }}
          >
            {props.children}
          </button>
        );
      }
      const workspacePath = (props as Record<string, unknown>)["data-workspace-path"];
      if (typeof workspacePath === "string" && workspacePath) {
        return (
          <a
            {...props}
            target="_blank"
            rel="noopener noreferrer"
            onClick={(event) => {
              // Plain click previews in-app; modifier clicks keep the
              // browser's open-in-new-tab behaviour.
              if (!onWorkspaceFileClick || event.metaKey || event.ctrlKey || event.shiftKey || event.button !== 0) return;
              event.preventDefault();
              onWorkspaceFileClick(workspacePath);
            }}
          />
        );
      }
      return <ExternalAnchor {...props} />;
    },
  }), [knowledgeByID, onKnowledgeCitationClick, onWorkspaceFileClick]);

  // Build the URL transform once per agent/session. A stable identity keeps
  // Streamdown (a memo component) from re-rendering on every streamed keystroke,
  // which a fresh inline function each render would defeat.
  const urlTransform = useMemo<UrlTransform>(() => {
    return (url, key, node) => {
      // Inline base64 images pass through (the default transform strips data:).
      if (key === "src" && url.startsWith("data:image/")) return url;
      // Workspace paths were already rewritten by rehypeWorkspaceUrls.
      return defaultUrlTransform(url, key, node);
    };
  }, []);

  // Default chain is [raw, sanitize, harden]; the workspace rewrite slots in
  // right before harden so harden validates the final /api/… URL.
  const rehypePlugins = useMemo<RehypePlugins>(() => {
    const { harden, ...rest } = defaultRehypePlugins;
    if (!agentId) return [...Object.values(rest), harden];
    const workspaceUrls = [rehypeWorkspaceUrls, { agentId, sessionId }] as RehypePlugins[number];
    return [...Object.values(rest), workspaceUrls, harden];
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
        rehypePlugins={rehypePlugins}
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
