import { useEffect, useState } from "react";
import { api, clearSession, loadSession, onUnauthorized, saveSession } from "./api.js";
import { Console } from "./Console.jsx";

export function App() {
  const [session, setSession] = useState(loadSession);
  const loggedIn = session.secret !== "";

  useEffect(() => {
    onUnauthorized(() => setSession({ secret: "", username: "" }));
  }, []);

  function login(secret, username) {
    saveSession(secret, username);
    setSession({ secret, username });
  }

  function logout() {
    clearSession();
    setSession({ secret: "", username: "" });
  }

  return loggedIn
    ? <Console username={session.username} onLogout={logout} />
    : <Gate onLogin={login} />;
}

function gateText(err) {
  switch (err.message) {
    case "username":
      return "请填写用户名";
    case "password":
      return "密码至少 8 个字符";
    case "duplicate_username":
      return "用户名已存在";
    case "unauthorized":
      return "用户名或密码不对";
    case "rejected":
      return "当前不能注册";
    case "config_unreadable":
      return "配置不可读时不能注册或登录。";
    default:
      return "操作没有完成";
  }
}

const blockedNote = { kind: "note", text: "配置不可读时不能注册或登录。" };

function Gate({ onLogin }) {
  const [screen, setScreen] = useState("login");
  const [pill, setPill] = useState("读取中");
  const [pillClass, setPillClass] = useState("pill");
  const [message, setMessage] = useState(null);
  const [blocked, setBlocked] = useState(false);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [loginUser, setLoginUser] = useState("");
  const [loginPassword, setLoginPassword] = useState("");

  useEffect(() => {
    api("GET", "/config-status").then((body) => {
      if (body.config === "unreadable") {
        setBlocked(true);
        setPill("配置不可读");
        setPillClass("pill bad");
        setMessage(blockedNote);
      }
    }).catch((err) => {
      if (err.status === 401) {
        setPill("未登录");
        setPillClass("pill");
      }
    });
  }, []);

  function openRegister() {
    setScreen("register");
    setPassword("");
    setMessage(blocked ? blockedNote : null);
  }

  function openLogin() {
    setScreen("login");
    setLoginPassword("");
    setMessage(blocked ? blockedNote : null);
  }

  async function register(event) {
    event.preventDefault();
    setMessage(null);
    try {
      const name = username.trim();
      await api("POST", "/register", { username: name, password });
      setLoginUser(name);
      setLoginPassword("");
      setScreen("login");
      setMessage({ kind: "note", text: "已注册，请登录" });
    } catch (err) {
      setMessage({ kind: "error", text: gateText(err) });
    }
  }

  async function login(event) {
    event.preventDefault();
    setMessage(null);
    try {
      const logged = await api("POST", "/login", { username: loginUser.trim(), password: loginPassword });
      onLogin(logged.secret, logged.username || loginUser.trim());
    } catch (err) {
      setMessage({ kind: "error", text: gateText(err) });
    }
  }

  return (
    <>
      <header>
        <h1>AIGate <span>控制台</span></h1>
        <div className="cred"><span className={pillClass}>{pill}</span></div>
      </header>
      <main className="gate">
        {message ? <p className={message.kind}>{message.text}</p> : null}
        {screen === "register" ? (
          <>
            <form className="grid" onSubmit={register}>
              <label>用户名<input value={username} placeholder="用户名" onChange={(e) => setUsername(e.target.value)} /></label>
              <label>密码<input type="password" value={password} placeholder="至少 8 个字符" onChange={(e) => setPassword(e.target.value)} /></label>
              <button type="submit">注册</button>
            </form>
            <p className="gate-switch">已有账号？<button type="button" className="link" onClick={openLogin}>去登录</button></p>
          </>
        ) : (
          <>
            <form className="grid" onSubmit={login}>
              <label>用户名<input value={loginUser} placeholder="用户名" onChange={(e) => setLoginUser(e.target.value)} /></label>
              <label>密码<input type="password" value={loginPassword} placeholder="密码" onChange={(e) => setLoginPassword(e.target.value)} /></label>
              <button type="submit">登录</button>
            </form>
            <p className="gate-switch">没有账号？<button type="button" className="link" onClick={openRegister}>去注册</button></p>
          </>
        )}
      </main>
    </>
  );
}
