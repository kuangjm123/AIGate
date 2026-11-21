import { useEffect, useState } from "react";
import { api, clearSession, loadSession, onUnauthorized, saveSession } from "./api.js";
import { Console } from "./Console.jsx";
import { Brand } from "./components/ui.jsx";

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
      } else {
        setPill("服务正常");
        setPillClass("pill ok");
      }
    }).catch((err) => {
      setPill(err.status === 401 ? "未登录" : "状态未知");
      setPillClass("pill");
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

  const registering = screen === "register";
  return <div className="auth-shell">
    <header className="auth-topbar"><Brand/><span className={pillClass}>{pill}</span></header>
    <main className="gate">
      <section className="auth-card">
        <div className="auth-heading"><span className="eyebrow">LOCAL AI GATEWAY</span><h1>{registering ? "创建控制台账号" : "欢迎回来"}</h1><p>{registering ? "注册一个本地账号，开始管理你的模型网关。" : "登录以管理模型、路由和访问凭证。"}</p></div>
        {message ? <p className={message.kind} role={message.kind === "error" ? "alert" : "status"}>{message.text}</p> : null}
        <form className="auth-form" onSubmit={registering ? register : login}>
          <label>用户名<input autoComplete="username" value={registering ? username : loginUser} placeholder="请输入用户名" onChange={(e) => registering ? setUsername(e.target.value) : setLoginUser(e.target.value)} required/></label>
          <label>密码<input type="password" autoComplete={registering ? "new-password" : "current-password"} value={registering ? password : loginPassword} placeholder={registering ? "至少 8 个字符" : "请输入密码"} onChange={(e) => registering ? setPassword(e.target.value) : setLoginPassword(e.target.value)} required/></label>
          <button type="submit" className="auth-submit">{registering ? "注册账号" : "登录控制台"}</button>
        </form>
        <p className="gate-switch">{registering ? "已有账号？" : "没有账号？"}<button type="button" className="link" onClick={registering ? openLogin : openRegister}>{registering ? "返回登录" : "创建账号"}</button></p>
      </section>
      <p className="auth-footnote">AIGate · 让每一次模型调用清晰可控</p>
    </main>
  </div>;
}
