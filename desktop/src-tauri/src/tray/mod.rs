pub struct TrayState {
    pub visible: bool,
}

impl TrayState {
    pub fn new() -> Self {
        Self { visible: true }
    }

    pub fn show_window(&mut self) {
        self.visible = true;
    }

    pub fn hide_window(&mut self) {
        self.visible = false;
    }
}
