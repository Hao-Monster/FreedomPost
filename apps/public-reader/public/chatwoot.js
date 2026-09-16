(function () {
  var baseUrl = "https://chat.clawpan.online";
  // Public WebWidget identifier for account 1 / inbox 1; not an API credential.
  var websiteToken = "WM32VohJAYcy69Rx6qr1P2Lt";
  window.chatwootSettings = { position: "right", type: "standard", launcherTitle: "" };
  var script = document.createElement("script");
  script.src = baseUrl + "/packs/js/sdk.js";
  script.async = true;
  script.onload = function () {
    window.chatwootSDK.run({ websiteToken: websiteToken, baseUrl: baseUrl });
  };
  document.head.appendChild(script);
}());
