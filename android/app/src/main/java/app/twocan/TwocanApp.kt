package app.twocan

import android.app.Application
import androidx.lifecycle.DefaultLifecycleObserver
import androidx.lifecycle.LifecycleOwner
import androidx.lifecycle.ProcessLifecycleOwner

class TwocanApp : Application() {
    lateinit var twocan: Twocan
        private set

    override fun onCreate() {
        super.onCreate()
        twocan = Twocan(this)
        ProcessLifecycleOwner.get().lifecycle.addObserver(object : DefaultLifecycleObserver {
            override fun onStart(owner: LifecycleOwner) = twocan.onForeground()
            override fun onStop(owner: LifecycleOwner) = twocan.onBackground()
        })
    }
}
