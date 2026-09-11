(function () {
  var baseUrl = "https://support-freedompost.openal.uk";
  var websiteToken = atob("eTlEd3JtZEVuWkh2SEZOZXBRZUZSczVD");
  window.chatwootSettings = { position: "right", type: "standard", launcherTitle: "" };
  var script = document.createElement("script");
  script.src = baseUrl + "/packs/js/sdk.js";
  script.async = true;
  script.onload = function () {
    window.chatwootSDK.run({ websiteToken: websiteToken, baseUrl: baseUrl });
  };
  document.head.appendChild(script);
}());
