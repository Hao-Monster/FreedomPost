(function () {
  var baseUrl = "https://chat.clawpan.online";
  // Public WebWidget identifier for account 1 / inbox 1; not an API credential.
  var websiteToken = atob("V00zMlZvaEpBWWN5NjlSeDZxcjFQMkx0");
  window.chatwootSettings = { position: "right", type: "standard", launcherTitle: "" };
  var script = document.createElement("script");
  script.src = baseUrl + "/packs/js/sdk.js";
  script.async = true;
  script.onload = function () {
    window.chatwootSDK.run({ websiteToken: websiteToken, baseUrl: baseUrl });
  };
  document.head.appendChild(script);
}());
