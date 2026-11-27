const sessionKey = "aigate.session";
const userKey = "aigate.username";

let unauthorized = () => {};

export function onUnauthorized(fn) {
  unauthorized = fn;
}

export function loadSession() {
  return {
    secret: sessionStorage.getItem(sessionKey) || "",
    username: sessionStorage.getItem(userKey) || "",
  };
}

export function saveSession(secret, username) {
  sessionStorage.setItem(sessionKey, secret);
  sessionStorage.setItem(userKey, username);
}

export function clearSession() {
  sessionStorage.removeItem(sessionKey);
  sessionStorage.removeItem(userKey);
}

export async function api(method, path, body) {
  const headers = {};
  const token = sessionStorage.getItem(sessionKey) || "";
  if (token) headers.Authorization = "Bearer " + token;
  let payload;
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    payload = JSON.stringify(body);
  }
  const res = await fetch(path, { method, headers, body: payload });
  const text = await res.text();
  let data = null;
  if (text) data = JSON.parse(text);
  if (!res.ok) {
    if (res.status === 401 && token) {
      clearSession();
      unauthorized();
    }
    const err = new Error((data && (data.reason || data.error)) || String(res.status));
    err.status = res.status;
    throw err;
  }
  return data;
}

export async function download(path, filename) {
  const headers = {};
  const token = sessionStorage.getItem(sessionKey) || "";
  if (token) headers.Authorization = "Bearer " + token;
  const res = await fetch(path, { headers });
  if (!res.ok) {
    if (res.status === 401 && token) {
      clearSession();
      unauthorized();
    }
    let data = null;
    try { data = await res.json(); } catch {}
    const err = new Error((data && (data.reason || data.error)) || String(res.status));
    err.status = res.status;
    throw err;
  }
  const blob = await res.blob();
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  document.body.appendChild(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
}

export function asList(value) {
  return Array.isArray(value) ? value : [];
}

export function splitList(value) {
  return value.split(",").map((s) => s.trim()).filter(Boolean);
}
