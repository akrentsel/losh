document.querySelectorAll("[data-copy]").forEach((button) => {
  button.addEventListener("click", async () => {
    const value = button.getAttribute("data-copy");
    try {
      await navigator.clipboard.writeText(value);
      button.textContent = "Copied";
      button.classList.add("copied");
      window.setTimeout(() => {
        button.textContent = "Copy";
        button.classList.remove("copied");
      }, 1600);
    } catch {
      button.textContent = "Select";
    }
  });
});
