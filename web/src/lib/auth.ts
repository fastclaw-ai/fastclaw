import { setAuthToken, getAuthToken } from "./api";

const TOKEN_KEY = "fastclaw_token";

export function isLoggedIn(): boolean {
  return !!getAuthToken();
}

export function login(token: string) {
  setAuthToken(token);
}

export function logout() {
  setAuthToken("");
  localStorage.removeItem(TOKEN_KEY);
}
