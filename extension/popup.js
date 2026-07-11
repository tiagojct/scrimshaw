const $ = id => document.getElementById(id);

chrome.storage.local.get(["origin", "token"], values => {
  $("origin").value = values.origin || "";
  $("token").value = values.token || "";
});

$("save").addEventListener("click", async () => {
  const origin = $("origin").value.replace(/\/$/, "");
  const token = $("token").value;
  const tags = $("tags").value.split(",").map(tag => tag.trim()).filter(Boolean);
  if (!origin || !token) { $("status").textContent = "Enter the server URL and token."; return; }
  const [tab] = await chrome.tabs.query({active: true, currentWindow: true});
  try {
    const response = await fetch(origin + "/api/save", {method: "POST", headers: {"Authorization": "Bearer " + token, "Content-Type": "application/json"}, body: JSON.stringify({url: tab.url, tags})});
    if (!response.ok) throw new Error("save failed");
    await chrome.storage.local.set({origin, token});
    $("status").textContent = "Saved.";
  } catch (_) { $("status").textContent = "Could not save this page."; }
});
