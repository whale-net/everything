package libs.scala

/** Shared utilities for Scala applications. */
object Utils {
  
  /** Format a greeting message.
    * @param name The name to greet
    * @return Formatted greeting string
    */
  def formatGreeting(name: String): String = {
    s"Hello, $name from Scala!"
  }
  
  /** Get the application version.
    * @return Version string
    */
  def getVersion: String = "1.0.0"
}
