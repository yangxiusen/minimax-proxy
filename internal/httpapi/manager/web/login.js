"use strict";

document.getElementById("login-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const submit = document.getElementById("login-submit");
  const error = document.getElementById("login-error");
  submit.disabled = true;
  error.textContent = "";
  try {
    const response = await fetch("/manager/api/session", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        username: document.getElementById("username").value,
        password: document.getElementById("password").value
      })
    });
    if (response.ok) {
      window.location.replace("/manager/");
      return;
    }
    error.textContent = response.status === 429 ? "登录尝试过于频繁，请稍后重试" : "账号或密码错误";
  } catch (_) {
    error.textContent = "暂时无法登录，请稍后重试";
  } finally {
    submit.disabled = false;
  }
});
