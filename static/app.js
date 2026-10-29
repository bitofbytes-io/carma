// Delegate to document so controls keep working after HTMX replaces the body.
document.addEventListener("change", function (event) {
  if (event.target.matches(".vehicle-switch") && event.target.value) {
    window.location.href = event.target.value;
  }
  if (event.target.matches("[data-history-costs]") && event.target.form) {
    event.target.form.submit();
  }
});

document.addEventListener("click", function (event) {
  if (event.target.closest("[data-print-history]")) {
    window.print();
  }
});
