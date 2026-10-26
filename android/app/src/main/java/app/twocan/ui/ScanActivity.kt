package app.twocan.ui

import com.journeyapps.barcodescanner.CaptureActivity

/**
 * The QR scanner. The library's own activity is fixed to landscape; this
 * one is declared without an orientation, so it turns with the phone.
 */
class ScanActivity : CaptureActivity()
