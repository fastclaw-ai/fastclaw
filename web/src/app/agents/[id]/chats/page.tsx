// /agents/<aid>/chats — full conversation list in ChatScreen's right panel.
// The parent agent layout owns the persistent ChatScreen instance; this file
// only gives Next's static export a route to match. Keeping the route page
// empty is important: browser-history navigation between /chats and
// /chat/<session> must never leave a second page tree below the chat canvas.
export default function AgentChatsPage() {
  return null;
}
