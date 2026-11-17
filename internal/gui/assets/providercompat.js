// Provider-specific compatibility fields are loaded with the editor.
window.providerCompatFill = function (box, draft, provider, custom) {
  if (provider ? provider.takesPassErrors : custom) {
    const button = el("button", "sess-pick pass-errors");
    import("./passerrors.js").then(() => passErrorsFill(button, draft), statusErr);
    box.append(...field(t("Errors"), button));
  }
  if (provider?.takesImageFiles) {
    const [row, checkbox] = tick(t("Upload images once and send them by file id"), !draft.imageFilesOff);
    row.classList.add("image-files");
    checkbox.onchange = () => { draft.imageFilesOff = !checkbox.checked; };
    box.append(...field(t("Images"), row, t("Off: an agent's images are sent inline on every turn.")));
  }
  if (custom) {
    const [row, checkbox] = tick(t("Send model in Gemini request body"), !!draft.geminiModelInBody);
    row.classList.add("gemini-model-body");
    checkbox.onchange = () => { draft.geminiModelInBody = checkbox.checked; };
    box.append(...field(t("Gemini"), row, t("For Gemini relays that require a model field beside the URL. Uses the routed upstream model, including when you switch models. Leave off for Google's API.")));
  }
};
